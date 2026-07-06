// Package sample is a tiny fixture for symbol extraction tests.
package sample

import (
	"context"
	"fmt"
)

// MaxItems is the maximum number of items.
const MaxItems = 100

// Item is one item.
type Item struct {
	Name  string
	Value int
}

// Processor does things.
type Processor struct{}

// Process processes an item.
func (p *Processor) Process(ctx context.Context, it Item) error {
	if it.Value < 0 {
		return fmt.Errorf("negative value: %d", it.Value)
	}
	return nil
}

// Helper is a package-level helper.
func Helper(items []Item) int {
	total := 0
	for _, it := range items {
		total += it.Value
	}
	return total
}
