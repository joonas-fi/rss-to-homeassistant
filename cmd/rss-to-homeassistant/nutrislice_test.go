package main

import (
	"testing"
	"time"
)

func TestNutrisliceToMarkdown(t *testing.T) {
	mockData := NutrisliceResponse{
		Days: []NutrisliceDay{
			{
				Date:      "2026-02-21", // Saturday
				MenuItems: []NutrisliceMenuItem{},
			},
			{
				Date: "2026-02-23", // Monday
				MenuItems: []NutrisliceMenuItem{
					{Text: "Monday Item", Food: &NutrisliceFood{Name: "Pizza"}},
				},
			},
		},
	}

	// Test Scenario C: Weekend (Saturday)
	// Saturday, Feb 21, 2026 at 10:00 AM
	satMorning := time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC)
	res := nutrisliceToMarkdown(mockData, satMorning)
	if res != "No Menu Today" {
		t.Errorf("Expected 'No Menu Today' for Saturday morning, got '%s'", res)
	}

	// Test Scenario B: Monday Morning (Before 2pm)
	monMorning := time.Date(2026, 2, 23, 10, 0, 0, 0, time.UTC)
	res = nutrisliceToMarkdown(mockData, monMorning)
	if res != "- Pizza" {
		t.Errorf("Expected '- Pizza' for Monday morning, got '%s'", res)
	}

	// Test Scenario B: Sunday Afternoon (After 2pm -> should pull Monday's menu)
	sunAfternoon := time.Date(2026, 2, 22, 15, 0, 0, 0, time.UTC)
	res = nutrisliceToMarkdown(mockData, sunAfternoon)
	if res != "- Pizza" {
		t.Errorf("Expected '- Pizza' for Sunday afternoon (tomorrow's menu), got '%s'", res)
	}

	// Test Empty Menu Items list (Scenario C)
	emptyData := NutrisliceResponse{
		Days: []NutrisliceDay{
			{Date: "2026-02-23", MenuItems: []NutrisliceMenuItem{}},
		},
	}
	res = nutrisliceToMarkdown(emptyData, monMorning)
	if res != "No Menu Today" {
		t.Errorf("Expected 'No Menu Today' for empty menu items list, got '%s'", res)
	}
}
