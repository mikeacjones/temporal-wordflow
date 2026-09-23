package activities

import (
	"context"
	"fmt"
	"sync"

	"go.temporal.io/sdk/client"
)

// TemporalClientProvider lazily creates one client shared by activities in a
// serverless worker invocation. The Lambda worker closes it after draining.
type TemporalClientProvider struct {
	options client.Options

	mu       sync.Mutex
	temporal client.Client
}

func NewTemporalClientProvider(options client.Options) *TemporalClientProvider {
	return &TemporalClientProvider{options: options}
}

func (p *TemporalClientProvider) Get() (client.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.temporal != nil {
		return p.temporal, nil
	}
	temporalClient, err := client.Dial(p.options)
	if err != nil {
		return nil, err
	}
	p.temporal = temporalClient
	return temporalClient, nil
}

func (p *TemporalClientProvider) Close(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.temporal != nil {
		p.temporal.Close()
		p.temporal = nil
	}
	return nil
}

func activityTemporalClient(direct client.Client, provider *TemporalClientProvider) (client.Client, error) {
	if direct != nil {
		return direct, nil
	}
	if provider == nil {
		return nil, fmt.Errorf("Temporal client is not configured")
	}
	return provider.Get()
}
