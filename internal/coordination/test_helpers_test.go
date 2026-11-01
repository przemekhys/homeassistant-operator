package coordination

import (
	"context"
	"errors"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

var errInjectedConflict = errors.New("injected CAS conflict")

type conflictOnceClient struct {
	client.Client
	mu       sync.Mutex
	conflict bool
}

func (c *conflictOnceClient) Update(ctx context.Context, obj client.Object, options ...client.UpdateOption) error {
	c.mu.Lock()
	if c.conflict {
		c.conflict = false
		c.mu.Unlock()
		return errInjectedConflict
	}
	c.mu.Unlock()
	return c.Client.Update(ctx, obj, options...)
}

func newTestStore(objects ...runtime.Object) Store {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return Store{Reader: c, Client: c}
}
