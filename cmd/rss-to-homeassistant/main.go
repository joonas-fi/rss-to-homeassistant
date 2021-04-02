// Sync state from Hautomo to Home Assistant, along with support for pushing remote URL
// changes (images / RSS feeds) to Home Assistant
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/function61/gokit/app/dynversion"
	"github.com/function61/gokit/encoding/jsonfile"
	"github.com/function61/gokit/log/logex"
	"github.com/function61/gokit/os/osutil"
	"github.com/function61/gokit/os/systemdinstaller"
	"github.com/joonas-fi/rss-to-homeassistant/pkg/homeassistant"
	"github.com/spf13/cobra"
)

const tagline = "Pushes RSS feeds into Home Assistant as markdown"

func main() {
	rootLogger := logex.StandardLogger()

	app := &cobra.Command{
		Use:     os.Args[0],
		Short:   tagline,
		Version: dynversion.Version,
		Run: func(_ *cobra.Command, _ []string) {
			osutil.ExitIfError(logic(
				osutil.CancelOnInterruptOrTerminate(rootLogger),
				rootLogger))
		},
	}

	app.AddCommand(&cobra.Command{
		Use:   "install-as-service",
		Short: "Keep this software running across system restarts",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			osutil.ExitIfError(func() error {
				service := systemdinstaller.Service(
					"rss-to-homeassistant",
					tagline,
					systemdinstaller.Docs(
						"https://github.com/joonas-fi/rss-to-homeassistant",
						"https://joonas.fi/"))

				if err := systemdinstaller.Install(service); err != nil {
					return err
				}

				fmt.Println(systemdinstaller.EnableAndStartCommandHints(service))

				return nil
			}())
		},
	})

	osutil.ExitIfError(app.Execute())
}

func logic(ctx context.Context, logger *log.Logger) error {
	conf, err := readConfigurationFile()
	if err != nil {
		return err
	}

	logl := logex.Levels(logger)

	ha, err := homeassistant.NewMqttClient(conf.MQTTAddr, logl)
	if err != nil {
		return fmt.Errorf("NewMqttClient: %w", err)
	}

	pollingTasks := []func(context.Context) error{}

	allEntities := []*homeassistant.Entity{}

	for _, feed := range conf.RSSFeeds {
		feedSensor, feedPollerTask := makeRssFeedSensor(feed, ha, logl)

		allEntities = append(allEntities, feedSensor)
		pollingTasks = append(pollingTasks, feedPollerTask)
	}

	// tell Home Assistant about our sensor entities
	if err := ha.AutodiscoverEntities(allEntities...); err != nil {
		return err
	}

	runPollingTasks := func() {
		_ = launchAndWaitMany(ctx, func(err error) {
			logl.Error.Println(err)
		}, pollingTasks...)
	}

	// so we don't have to wait the *pollInterval* for the initial sync
	runPollingTasks()

	pollInterval := time.NewTicker(1 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pollInterval.C:
			runPollingTasks()
		}
	}
}

type configRSSFeed struct {
	Id       string        `json:"id"`
	URL      string        `json:"url"`
	Settings *feedSettings `json:"settings"`
}

type feedSettings struct {
	ItemDisplayLimit int `json:"item_display_limit"`
}

type config struct {
	MQTTAddr string          `json:"mqtt_addr"`
	RSSFeeds []configRSSFeed `json:"rss_feeds"`
}

func readConfigurationFile() (*config, error) {
	conf := &config{}
	if err := jsonfile.ReadDisallowUnknownFields("config.json", &conf); err != nil {
		return nil, err
	}

	for _, rssFeed := range conf.RSSFeeds {
		// Home Assistant tolerates this but will silently translate to '_'.
		// but we want to be explicit to avoid confusion.
		if strings.Contains(rssFeed.Id, "-") {
			return nil, errors.New("RSS feed ID cannot contain '-'")
		}
	}

	return conf, nil
}
