package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	format := feedConfig.Format
	if format == "" {
		format = "auto"
	}

	// Auto-detection logic
	if format == "auto" {
		if strings.Contains(feedConfig.URL, "nutrislice.com") {
			format = "json"
		} else {
			// For now, assume RSS if not Nutrislice, or we can peek at headers if needed.
			// But the spec says "or Nutrislice.com in URL".
			// Let's refine this to check Content-Type if we wanted to be more robust.
			format = "rss"
		}
	}

	if format == "json" {
		return fetchNutrisliceToMarkdown(ctx, feedConfig)
	}

	// Legacy RSS path
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

type NutrisliceResponse struct {
	Days []NutrisliceDay `json:"days"`
}

type NutrisliceDay struct {
	Date      string               `json:"date"`
	MenuItems []NutrisliceMenuItem `json:"menu_items"`
}

type NutrisliceMenuItem struct {
	Text string          `json:"text"`
	Food *NutrisliceFood `json:"food"`
}

type NutrisliceFood struct {
	Name string `json:"name"`
}

var nutrisliceDateRegexp = regexp.MustCompile(`\d{4}/\d{2}/\d{2}`)

func fetchNutrisliceToMarkdown(ctx context.Context, feedConfig configRSSFeed) (*gofeed.Feed, string, error) {
	now := time.Now()
	targetDate := now
	if now.Hour() >= 14 { // Past 2pm, use tomorrow's date
		targetDate = now.AddDate(0, 0, 1)
	}

	url := feedConfig.URL
	// Automatically replace date in URL if it matches Nutrislice pattern
	url = nutrisliceDateRegexp.ReplaceAllString(url, targetDate.Format("2006/01/02"))

	res, err := ezhttp.Get(ctx, url)
	if err != nil {
		return nil, "", fmt.Errorf("Menu Unavailable: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		return nil, "", fmt.Errorf("Menu Unavailable: HTTP %d", res.StatusCode)
	}

	contentType := res.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return nil, "", fmt.Errorf("Menu Unavailable: Expected JSON but got %s", contentType)
	}

	var nutrislice NutrisliceResponse
	if err := json.NewDecoder(res.Body).Decode(&nutrislice); err != nil {
		return nil, "", fmt.Errorf("Menu Unavailable: JSON decode error: %w", err)
	}

	return &gofeed.Feed{Title: "Menu"}, nutrisliceToMarkdown(nutrislice, now), nil
}

func nutrisliceToMarkdown(nutrislice NutrisliceResponse, now time.Time) string {
	targetDate := now
	if now.Hour() >= 14 { // Past 2pm, use tomorrow's date
		targetDate = now.AddDate(0, 0, 1)
	}

	targetDateStr := targetDate.Format("2006-01-02")
	var targetMenuItems []NutrisliceMenuItem

	for _, day := range nutrislice.Days {
		if day.Date == targetDateStr {
			targetMenuItems = day.MenuItems
			break
		}
	}

	if len(targetMenuItems) == 0 {
		return "No Menu Today"
	}

	lines := []string{}
	for _, item := range targetMenuItems {
		name := ""
		if item.Food != nil && item.Food.Name != "" {
			name = item.Food.Name
		} else if item.Text != "" {
			name = item.Text
		}

		if name != "" {
			lines = append(lines, "- "+name)
		}
	}

	if len(lines) == 0 {
		return "No Menu Today"
	}

	return strings.Join(lines, "\n")
}
