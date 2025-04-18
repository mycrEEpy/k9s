// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/ui"
	"github.com/derailed/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestTableSave(t *testing.T) {
	v := NewTable(client.NewGVR("test"))
	require.NoError(t, v.Init(makeContext(t)))
	v.SetTitle("k9s-test")

	require.NoError(t, ensureDumpDir("/tmp/test-dumps"))
	dir := v.app.Config.K9s.ContextScreenDumpDir()
	c1, _ := os.ReadDir(dir)
	v.saveCmd(nil)

	c2, _ := os.ReadDir(dir)
	assert.Len(t, c2, len(c1)+1)
}

func TestTableNew(t *testing.T) {
	v := NewTable(client.NewGVR("test"))
	require.NoError(t, v.Init(makeContext(t)))

	data := model1.NewTableDataWithRows(
		client.NewGVR("test"),
		model1.Header{
			model1.HeaderColumn{Name: "NAMESPACE"},
			model1.HeaderColumn{Name: "NAME", Attrs: model1.Attrs{Align: tview.AlignRight}},
			model1.HeaderColumn{Name: "FRED"},
			model1.HeaderColumn{Name: "AGE", Attrs: model1.Attrs{Time: true, Decorator: render.AgeDecorator}},
		},
		model1.NewRowEventsWithEvts(
			model1.RowEvent{
				Row: model1.Row{
					Fields: model1.Fields{"ns1", "a", "10", "3m"},
				},
			},
			model1.RowEvent{
				Row: model1.Row{
					Fields: model1.Fields{"ns1", "b", "15", "1m"},
				},
			},
		),
	)
	cdata := v.Update(data, false)
	v.UpdateUI(cdata, data)

	assert.Equal(t, 3, v.GetRowCount())
}

func TestTableViewFilter(t *testing.T) {
	v := NewTable(client.NewGVR("test"))
	require.NoError(t, v.Init(makeContext(t)))
	v.SetModel(&mockTableModel{})
	v.Refresh()

	v.CmdBuff().SetActive(true)
	v.CmdBuff().SetText("blee", "")

	assert.Equal(t, 5, v.GetRowCount())
}

func TestTableViewSort(t *testing.T) {
	v := NewTable(client.NewGVR("test"))
	require.NoError(t, v.Init(makeContext(t)))
	v.SetModel(new(mockTableModel))

	uu := map[string]struct {
		sortCol  string
		sorted   []string
		reversed []string
	}{
		"by_name": {
			sortCol:  "NAME",
			sorted:   []string{"r0", "r1", "r2", "r3"},
			reversed: []string{"r3", "r2", "r1", "r0"},
		},
		"by_age": {
			sortCol:  "AGE",
			sorted:   []string{"r0", "r1", "r2", "r3"},
			reversed: []string{"r3", "r2", "r1", "r0"},
		},
		"by_fred": {
			sortCol:  "FRED",
			sorted:   []string{"r3", "r2", "r0", "r1"},
			reversed: []string{"r1", "r0", "r2", "r3"},
		},
	}

	for k := range uu {
		u := uu[k]
		v.SortColCmd(u.sortCol, true)(nil)
		assert.Len(t, u.sorted, v.GetRowCount()-1)
		for i, s := range u.sorted {
			assert.Equal(t, s, v.GetCell(i+1, 0).Text)
		}
		v.SortInvertCmd(nil)
		assert.Len(t, u.reversed, v.GetRowCount()-1)
		for i, s := range u.reversed {
			assert.Equal(t, s, v.GetCell(i+1, 0).Text)
		}
	}
}

// ----------------------------------------------------------------------------
// Helpers...

type mockTableModel struct{}

var _ ui.Tabular = (*mockTableModel)(nil)

func (*mockTableModel) SetViewSetting(context.Context, *config.ViewSetting) {}
func (*mockTableModel) SetInstance(string)                                  {}
func (*mockTableModel) SetLabelSelector(labels.Selector)                    {}
func (*mockTableModel) GetLabelSelector() labels.Selector                   { return nil }
func (*mockTableModel) Empty() bool                                         { return false }
func (*mockTableModel) RowCount() int                                       { return 1 }
func (*mockTableModel) HasMetrics() bool                                    { return true }
func (*mockTableModel) Peek() *model1.TableData                             { return makeTableData() }
func (*mockTableModel) Refresh(context.Context) error                       { return nil }
func (*mockTableModel) ClusterWide() bool                                   { return false }
func (*mockTableModel) GetNamespace() string                                { return "blee" }
func (*mockTableModel) SetNamespace(string)                                 {}
func (*mockTableModel) ToggleToast()                                        {}
func (*mockTableModel) AddListener(model.TableListener)                     {}
func (*mockTableModel) RemoveListener(model.TableListener)                  {}
func (*mockTableModel) Watch(context.Context) error                         { return nil }
func (*mockTableModel) Get(context.Context, string) (runtime.Object, error) {
	return nil, nil
}
func (*mockTableModel) Delete(context.Context, string, *metav1.DeletionPropagation, dao.Grace) error {
	return nil
}
func (*mockTableModel) Describe(context.Context, string) (string, error) {
	return "", nil
}
func (*mockTableModel) ToYAML(context.Context, string) (string, error) {
	return "", nil
}
func (*mockTableModel) InNamespace(string) bool      { return true }
func (*mockTableModel) SetRefreshRate(time.Duration) {}

func makeTableData() *model1.TableData {
	return model1.NewTableDataWithRows(
		client.NewGVR("test"),
		model1.Header{
			model1.HeaderColumn{Name: "NAMESPACE"},
			model1.HeaderColumn{Name: "NAME", Attrs: model1.Attrs{Align: tview.AlignRight}},
			model1.HeaderColumn{Name: "FRED"},
			model1.HeaderColumn{Name: "AGE", Attrs: model1.Attrs{Time: true}},
		},
		model1.NewRowEventsWithEvts(
			model1.RowEvent{
				Row: model1.Row{
					Fields: model1.Fields{"ns1", "r3", "10", "3y125d"},
				},
			},
			model1.RowEvent{
				Row: model1.Row{
					Fields: model1.Fields{"ns1", "r2", "15", "2y12d"},
				},
				Deltas: model1.DeltaRow{"", "", "20", ""},
			},
			model1.RowEvent{
				Row: model1.Row{
					Fields: model1.Fields{"ns1", "r1", "20", "19h"},
				},
			},
			model1.RowEvent{
				Row: model1.Row{
					Fields: model1.Fields{"ns1", "r0", "15", "10s"},
				},
			},
		),
	)
}

func makeContext(t *testing.T) context.Context {
	a := NewApp(mock.NewMockConfig(t))
	ctx := context.WithValue(context.Background(), internal.KeyApp, a)
	return context.WithValue(ctx, internal.KeyStyles, a.Styles)
}

func ensureDumpDir(n string) error {
	config.AppDumpsDir = n
	if _, err := os.Stat(n); errors.Is(err, fs.ErrNotExist) {
		return os.Mkdir(n, 0700)
	}
	if err := os.RemoveAll(n); err != nil {
		return err
	}
	return os.Mkdir(n, 0700)
}
