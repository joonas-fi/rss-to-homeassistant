package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/function61/gokit/log/logex"
	"github.com/function61/gokit/net/http/ezhttp"
	"github.com/function61/gokit/strings/stringutils"
	"github.com/function61/hautomo/pkg/changedetector"
	"github.com/function61/hautomo/pkg/homeassistant"
	"github.com/mmcdole/gofeed"
)

var topicPrefix = homeassistant.NewTopicPrefix("rss-to-homeassistant")

// makeRssFeedSensor creates a sensor entity for Home Assistant and returns a polling function
// that checks for changes in the feed and updates Home Assistant when changes are detected.
// The polling function is designed to be called by the feedPoller at the configured interval.
func makeRssFeedSensor(
	feedConfig configRSSFeed,
	ha *homeassistant.MqttClient,
	logl *logex.Leveled,
) (*homeassistant.Entity, func(context.Context) error) {
	// need attribute topic, see comment later
	sensor := homeassistant.NewSensorEntity(
		feedConfig.Id,
		"rss_"+feedConfig.Id,
		homeassistant.DiscoveryOptions{
			UniqueId:            "rss-" + feedConfig.Id,
			StateTopic:          topicPrefix.StateTopic(feedConfig.Id), // we don't use state, but this is required
			JsonAttributesTopic: topicPrefix.AttributesTopic(feedConfig.Id),
		})

	// TODO: we could use HTTP caching mechanism here
	rssChangeDetector := changedetector.New()

	return sensor, func(ctx context.Context) error {
		withErr := func(err error) error { return fmt.Errorf("%s: %w", feedConfig.Id, err) }

		feed, feedAsMarkdown, err := fetchRSSFeedToMarkdown(ctx, feedConfig)
		if err != nil {
			return withErr(err)
		}

		changed, err := rssChangeDetector.ReaderChanged(strings.NewReader(feedAsMarkdown))
		if err != nil {
			return withErr(err)
		}

		if !changed {
			return nil
		}

		logl.Info.Printf("%s changed", feedConfig.Id)

		// need to store content as an attribute, because state is capped at 256 chars
		if err := <-ha.PublishAttributes(sensor, map[string]interface{}{
			"title": feed.Title, // in case user wants to display the title dynamically from the feed
			"md":    feedAsMarkdown,
			"url":   feedConfig.URL,
		}); err != nil {
			return withErr(err)
		}

		return nil
	}
}

func feedToMarkdownList(feed *gofeed.Feed, maxItems int, maxLineLength int) string {
	lines := []string{}
	line := func(l string) {
		lines = append(lines, l)
	}

	for _, item := range feed.Items {
		line(fmt.Sprintf("- [%s](%s)", stringutils.Truncate(item.Title, maxLineLength), item.Link))

		if len(lines) >= maxItems {
			break
		}
	}

	return strings.Join(lines, "\n")
}

func fetchRSSFeed(ctx context.Context, feedUrl string) (*gofeed.Feed, error) {
	res, err := ezhttp.Get(ctx, feedUrl)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	return gofeed.NewParser().Parse(res.Body)
}

func fetchRSSFeedToMarkdown(ctx context.Context, feedConfig configRSSFeed) (*gofeed.Feed, string, error) {
	feed, err := fetchRSSFeed(ctx, feedConfig.URL)
	if err != nil {
		return nil, "", err
	}

	itemDisplayLimit := func() int {
		if feedConfig.Settings != nil {
			return feedConfig.Settings.ItemDisplayLimit
		} else {
			return 8
		}
	}()

	return feed, feedToMarkdownList(feed, itemDisplayLimit, 100), nil
}
