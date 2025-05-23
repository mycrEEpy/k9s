// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	backoff "github.com/cenkalti/backoff/v4"
	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/slogs"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const initRefreshRate = 300 * time.Millisecond

// TableListener represents a table model listener.
type TableListener interface {
	// TableNoData notifies listener no data was found.
	TableNoData(*model1.TableData)

	// TableDataChanged notifies the model data changed.
	TableDataChanged(*model1.TableData)

	// TableLoadFailed notifies the load failed.
	TableLoadFailed(error)
}

// Table represents a table model.
type Table struct {
	gvr           *client.GVR
	data          *model1.TableData
	listeners     []TableListener
	inUpdate      int32
	refreshRate   time.Duration
	instance      string
	labelSelector labels.Selector
	mx            sync.RWMutex
	vs            *config.ViewSetting
}

// NewTable returns a new table model.
func NewTable(gvr *client.GVR) *Table {
	return &Table{
		gvr:         gvr,
		data:        model1.NewTableData(gvr),
		refreshRate: 2 * time.Second,
	}
}

func (t *Table) SetViewSetting(ctx context.Context, vs *config.ViewSetting) {
	t.mx.Lock()
	t.vs = vs
	t.mx.Unlock()

	if ctx != context.Background() {
		if err := t.reconcile(ctx); err != nil {
			slog.Error("Refresh failed", slogs.GVR, t.gvr)
		}
	}
}

// SetLabelSelector sets the labels selector.
func (t *Table) SetLabelSelector(sel labels.Selector) {
	t.mx.Lock()
	defer t.mx.Unlock()

	t.labelSelector = sel
}

// GetLabelSelector sets the labels selector.
func (t *Table) GetLabelSelector() labels.Selector {
	t.mx.Lock()
	defer t.mx.Unlock()

	return t.labelSelector
}

// SetInstance sets a single entry table.
func (t *Table) SetInstance(path string) {
	t.instance = path
}

// AddListener adds a new model listener.
func (t *Table) AddListener(l TableListener) {
	t.mx.Lock()
	defer t.mx.Unlock()

	t.listeners = append(t.listeners, l)
}

// RemoveListener delete a listener from the list.
func (t *Table) RemoveListener(l TableListener) {
	victim := -1
	for i, lis := range t.listeners {
		if lis == l {
			victim = i
			break
		}
	}

	if victim >= 0 {
		t.mx.Lock()
		t.listeners = append(t.listeners[:victim], t.listeners[victim+1:]...)
		t.mx.Unlock()
	}
}

// Watch initiates model updates.
func (t *Table) Watch(ctx context.Context) error {
	if err := t.refresh(ctx); err != nil {
		return err
	}
	go t.updater(ctx)

	return nil
}

// Refresh updates the table content.
func (t *Table) Refresh(ctx context.Context) error {
	return t.refresh(ctx)
}

// Get returns a resource instance if found, else an error.
func (t *Table) Get(ctx context.Context, path string) (runtime.Object, error) {
	meta, err := getMeta(ctx, t.gvr)
	if err != nil {
		return nil, err
	}

	return meta.DAO.Get(ctx, path)
}

// Delete deletes a resource.
func (t *Table) Delete(ctx context.Context, path string, propagation *metav1.DeletionPropagation, grace dao.Grace) error {
	meta, err := getMeta(ctx, t.gvr)
	if err != nil {
		return err
	}

	nuker, ok := meta.DAO.(dao.Nuker)
	if !ok {
		return fmt.Errorf("no nuker for %q", meta.DAO.GVR())
	}

	return nuker.Delete(ctx, path, propagation, grace)
}

// GetNamespace returns the model namespace.
func (t *Table) GetNamespace() string {
	return t.data.GetNamespace()
}

// SetNamespace sets up model namespace.
func (t *Table) SetNamespace(ns string) {
	t.data.Reset(ns)
}

// InNamespace checks if current namespace matches desired namespace.
func (t *Table) InNamespace(ns string) bool {
	return t.data.GetNamespace() == ns && !t.data.Empty()
}

// SetRefreshRate sets model refresh duration.
func (t *Table) SetRefreshRate(d time.Duration) {
	t.refreshRate = d
}

// ClusterWide checks if resource is scope for all namespaces.
func (t *Table) ClusterWide() bool {
	return client.IsClusterWide(t.data.GetNamespace())
}

// Empty returns true if no model data.
func (t *Table) Empty() bool {
	return t.data.Empty()
}

// RowCount returns the row count.
func (t *Table) RowCount() int {
	return t.data.RowCount()
}

// Peek returns model data.
func (t *Table) Peek() *model1.TableData {
	t.mx.RLock()
	defer t.mx.RUnlock()

	return t.data.Clone()
}

func (t *Table) updater(ctx context.Context) {
	bf := backoff.NewExponentialBackOff()
	bf.InitialInterval, bf.MaxElapsedTime = initRefreshRate, maxReaderRetryInterval
	rate := initRefreshRate
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(rate):
			rate = t.refreshRate
			err := backoff.Retry(func() error {
				if err := t.refresh(ctx); err != nil {
					slog.Error("Refresh failed", slogs.GVR, t.gvr)
					return err
				}
				return nil
			}, backoff.WithContext(bf, ctx))
			if err != nil {
				slog.Warn("Reconciler exited", slogs.Error, err)
				t.fireTableLoadFailed(err)
				return
			}
		}
	}
}

func (t *Table) refresh(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&t.inUpdate, 0, 1) {
		slog.Debug("Dropping update...")
		return nil
	}
	defer atomic.StoreInt32(&t.inUpdate, 0)

	if err := t.reconcile(ctx); err != nil {
		return err
	}
	data := t.Peek()
	if data.RowCount() == 0 {
		t.fireNoData(data)
	} else {
		t.fireTableChanged(data)
	}

	return nil
}

func (t *Table) list(ctx context.Context, a dao.Accessor) ([]runtime.Object, error) {
	factory, ok := ctx.Value(internal.KeyFactory).(dao.Factory)
	if !ok {
		return nil, fmt.Errorf("expected Factory in context but got %T", ctx.Value(internal.KeyFactory))
	}
	a.Init(factory, t.gvr)

	t.mx.RLock()
	ctx = context.WithValue(ctx, internal.KeyLabels, t.labelSelector)
	t.mx.RUnlock()

	ns := client.CleanseNamespace(t.data.GetNamespace())
	if client.IsClusterScoped(ns) {
		ns = client.BlankNamespace
	}

	return a.List(ctx, ns)
}

func (t *Table) reconcile(ctx context.Context) error {
	var (
		oo  []runtime.Object
		err error
	)
	meta := resourceMeta(t.gvr)
	if t.vs != nil {
		meta.DAO.SetIncludeObject(true)
	}
	ctx = context.WithValue(ctx, internal.KeyLabels, t.labelSelector)
	if t.instance == "" {
		oo, err = t.list(ctx, meta.DAO)
	} else {
		o, e := t.Get(ctx, t.instance)
		oo, err = []runtime.Object{o}, e
	}
	if err != nil {
		return err
	}
	r := meta.Renderer
	r.SetViewSetting(t.vs)

	return t.data.Render(ctx, meta.Renderer, oo)
}

func (t *Table) fireTableChanged(data *model1.TableData) {
	var ll []TableListener
	t.mx.RLock()
	ll = t.listeners
	t.mx.RUnlock()

	for _, l := range ll {
		l.TableDataChanged(data)
	}
}

func (t *Table) fireNoData(data *model1.TableData) {
	var ll []TableListener
	t.mx.RLock()
	ll = t.listeners
	t.mx.RUnlock()

	for _, l := range ll {
		l.TableNoData(data)
	}
}

func (t *Table) fireTableLoadFailed(err error) {
	var ll []TableListener
	t.mx.RLock()
	ll = t.listeners
	t.mx.RUnlock()

	for _, l := range ll {
		l.TableLoadFailed(err)
	}
}
