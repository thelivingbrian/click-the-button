package main

import (
	"sync"
	"time"
)

type Broadcaster struct {
	sync.Mutex
	listeners map[chan Point]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{listeners: make(map[chan Point]struct{})}
}

func (b *Broadcaster) Subscribe() chan Point {
	ch := make(chan Point, 100) // Remove buffer ?
	b.Lock()
	b.listeners[ch] = struct{}{}
	b.Unlock()
	return ch
}

func (b *Broadcaster) Unsubscribe(ch chan Point) {
	b.Lock()
	delete(b.listeners, ch)
	b.Unlock()
	close(ch)
}

func (b *Broadcaster) Publish(p Point) {
	b.Lock()
	defer b.Unlock()
	for ch := range b.listeners {
		select {
		case ch <- p:
			// noop
		default:
			// Channel buffer is full, skip sending
		}
	}
}

func (app *App) sendPeriodicBroadcasts() {
	if !app.configuration.broadcastEnabled() {
		return
	}
	go func() {
		ticker := time.NewTicker(app.configuration.broadcastInterval)
		defer ticker.Stop()

		var previousClickACount, previousClickBCount int64
		for range ticker.C {
			currentClicksA := app.clicksA.Load()
			currentClicksB := app.clicksB.Load()
			if currentClicksA == previousClickACount &&
				currentClicksB == previousClickBCount {
				continue
			}
			app.broadcaster.Publish(Point{
				Ts:      time.Now().UTC().Unix(),
				ClicksA: currentClicksA,
				ClicksB: currentClicksB,
			})
			previousClickACount, previousClickBCount = currentClicksA, currentClicksB
		}
	}()
}
