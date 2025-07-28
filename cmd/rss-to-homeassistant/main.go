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
	"github.com/function61/gokit/sync/taskrunner"
	"github.com/function61/hautomo/pkg/homeassistant"
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

	app.AddCommand(&cobra.Command{
		Use:   "debug [feedURL]",
		Short: "If a feed URL is not producing content, debug why",
		Args:  cobra.ExactArgs(1),
		Run: func(_ *cobra.Command, args []string) {
			osutil.ExitIfError(func() error {
				feedURL := args[0]

				_, output, err := fetchRSSFeedToMarkdown(context.Background(), configRSSFeed{URL: feedURL})
				if err != nil {
					return err
				}

				fmt.Println(output)
				return nil
			}())
		},
	})

	osutil.ExitIfError(app.Execute())
}

func logic(ctx context.Context, logger *log.Logger) error {
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	conf, err := readConfigurationFile()
	if err != nil {
		return err
	}

	logl := logex.Levels(logger)

	ha, haMqttTask := homeassistant.NewMQTTClient(conf.MQTT, "rss-to-homeassistant-"+hostname, logl)

	tasks := taskrunner.New(ctx, logger)
	tasks.Start("homeassistant-mqtt", haMqttTask)
	tasks.Start("feed-pollers", func(ctx context.Context) error {
		// Create entities and start pollers for each feed
		for _, feed := range conf.RSSFeeds {
			feed := feed // Create a new variable for the goroutine
			feedSensor, pollFunc := makeRssFeedSensor(feed, ha, logl)

			// Tell Home Assistant about this feed
			if err := ha.AutodiscoverEntities(feedSensor); err != nil {
				logl.Error.Printf("Failed to autodiscover feed %s: %v", feed.Id, err)
				continue
			}

			// Create and start the poller for this feed
			poller := newFeedPoller(feed, ha, logl, pollFunc)
			go poller.start(ctx)

			logl.Info.Printf("Started polling feed %s with interval %s", feed.Id, feed.PollInterval)
		}

		// Block until context is cancelled
		<-ctx.Done()
		return nil
	})

	return tasks.Wait()
}

type configRSSFeed struct {
	Id           string        `json:"id"`
	URL          string        `json:"url"`
	PollInterval string        `json:"poll_interval,omitempty"`
	Settings     *feedSettings `json:"settings,omitempty"`
}

type feedSettings struct {
	ItemDisplayLimit int `json:"item_display_limit"`
}

type config struct {
	MQTT     homeassistant.MQTTConfig `json:"mqtt"`
	RSSFeeds []configRSSFeed          `json:"rss_feeds"`
}

const (
	defaultPollInterval = "1m"
	minPollInterval     = 10 * time.Second
	maxPollInterval     = 24 * time.Hour
)

func readConfigurationFile() (*config, error) {
	conf := &config{}
	if err := jsonfile.ReadDisallowUnknownFields("config.json", &conf); err != nil {
		return nil, err
	}

	for i := range conf.RSSFeeds {
		rssFeed := &conf.RSSFeeds[i]

		// Home Assistant tolerates this but will silently translate to '_'.
		// but we want to be explicit to avoid confusion.
		if strings.Contains(rssFeed.Id, "-") {
			return nil, errors.New("RSS feed ID cannot contain '-'")
		}

		// Set default poll interval if not specified
		if rssFeed.PollInterval == "" {
			rssFeed.PollInterval = defaultPollInterval
		}

		// Validate poll interval
		duration, err := time.ParseDuration(rssFeed.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("invalid poll_interval '%s' for feed '%s': %v", rssFeed.PollInterval, rssFeed.Id, err)
		}

		if duration < minPollInterval {
			return nil, fmt.Errorf("poll_interval for feed '%s' cannot be less than %v", rssFeed.Id, minPollInterval)
		}

		if duration > maxPollInterval {
			return nil, fmt.Errorf("poll_interval for feed '%s' cannot be greater than %v", rssFeed.Id, maxPollInterval)
		}

		rssFeed.PollInterval = duration.String() // Normalize the duration string
	}

	return conf, nil
}
