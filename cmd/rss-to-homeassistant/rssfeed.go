package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/function61/gokit/log/logex"
	"github.com/function61/gokit/net/http/ezhttp"
	"github.com/function61/gokit/strings/stringutils"
	"github.com/joonas-fi/rss-to-homeassistant/pkg/homeassistant"
	"github.com/mmcdole/gofeed"
)

// makes sensor entity for advertising via autodiscovery, and a re-runnable task that checks for
// changes in the feed, and if it has it publishes the changed markdown to Home Assistant
func makeRssFeedSensor(
	entityId string,
	feedUrl string,
	ha *homeassistant.MqttClient,
	logl *logex.Leveled,
) (*homeassistant.Entity, func(context.Context) error) {
	// need attribute topic, see comment later
	sensor := homeassistant.NewSensor(
		entityId,
		feedUrl,
		homeassistant.DeviceClassDefault,
		true)

	// TODO: we could use HTTP caching mechanism here
	rssChangeDetector := &valueChangeDetector{}

	return sensor, func(ctx context.Context) error {
		feed, err := fetchRSSFeedItems(ctx, feedUrl)
		if err != nil {
			return err
		}

		feedAsMarkdown := feedToMarkdownList(feed, 8, 100)

		if !rssChangeDetector.Changed(feedAsMarkdown) {
			return nil
		}

		logl.Info.Printf("%s changed", entityId)

		// need to store content as an attribute, because state is capped at 256 chars
		return ha.PublishAttributes(sensor, map[string]string{
			"title": feed.Title, // in case user wants to display the title dynamically from the feed
			"md":    feedAsMarkdown,
		})
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

func fetchRSSFeedItems(ctx context.Context, feedUrl string) (*gofeed.Feed, error) {
	res, err := ezhttp.Get(ctx, feedUrl)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	return gofeed.NewParser().Parse(res.Body)
}
