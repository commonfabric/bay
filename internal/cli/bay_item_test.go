package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
)

func TestSplitItemArgs(t *testing.T) {
	cases := []struct {
		args     []string
		target   string
		name     string
		haveName bool
	}{
		{nil, "self", "", false},
		{[]string{"b3"}, "b3", "", false},
		{[]string{"docket"}, "self", "docket", true},
		{[]string{"self", "b3"}, "self", "b3", true},
		{[]string{"b2", "docket"}, "b2", "docket", true},
	}
	for _, c := range cases {
		target, name, have := splitItemArgs(c.args)
		if target != c.target || name != c.name || have != c.haveName {
			t.Errorf("%v = %q %q %v, want %q %q %v", c.args, target, name, have, c.target, c.name, c.haveName)
		}
	}
}

func itemFixture(t *testing.T) *engine.Engine {
	t.Helper()
	eng, _, _, _ := testNavEngine(t)
	for i := 0; i < 2; i++ {
		if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
			t.Fatal(err)
		}
	}
	return eng
}

func TestRunItem_SetReadClear(t *testing.T) {
	eng := itemFixture(t)
	var out bytes.Buffer
	if err := runItem(eng, &out, []string{"b1", "docket"}, itemFlags{dock: "labs"}); err != nil {
		t.Fatal(err)
	}
	if err := runItem(eng, &out, []string{"b1"}, itemFlags{dock: "labs"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "docket" {
		t.Fatalf("read = %q", got)
	}
	if err := runItem(eng, &out, []string{"b1", "other"}, itemFlags{dock: "labs"}); err == nil {
		t.Error("changing the item without --replace should fail")
	}
	if err := runItem(eng, &out, []string{"b1", "other"}, itemFlags{dock: "labs", repl: true}); err != nil {
		t.Errorf("--replace: %v", err)
	}
	if err := runItem(eng, &out, []string{"b1"}, itemFlags{dock: "labs", clr: true}); err != nil {
		t.Fatal(err)
	}
	if item, _, _ := eng.BayItem("labs", "b1"); item != "" {
		t.Errorf("item after clear = %q", item)
	}
}

func TestRunItem_FlagMisuse(t *testing.T) {
	eng := itemFixture(t)
	var out bytes.Buffer
	for name, c := range map[string]struct {
		args []string
		f    itemFlags
	}{
		"clear with name":   {[]string{"b1", "docket"}, itemFlags{dock: "labs", clr: true}},
		"clear with role":   {[]string{"b1"}, itemFlags{dock: "labs", clr: true, roleSet: true}},
		"role without name": {[]string{"b1"}, itemFlags{dock: "labs", roleSet: true, role: "x"}},
		"replace w/o name":  {[]string{"b1"}, itemFlags{dock: "labs", repl: true}},
		"bad name":          {[]string{"b1", "Not Valid"}, itemFlags{dock: "labs"}},
		"home pseudo-bay":   {[]string{"home", "docket"}, itemFlags{dock: "labs"}},
		"clear on home":     {[]string{"home"}, itemFlags{dock: "labs", clr: true}},
		"unknown bay":       {[]string{"b9", "docket"}, itemFlags{dock: "labs"}},
	} {
		if err := runItem(eng, &out, c.args, c.f); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRunItem_NameLikeBayID(t *testing.T) {
	eng := itemFixture(t)
	var out bytes.Buffer
	if err := runItem(eng, &out, []string{"b1", "b7"}, itemFlags{dock: "labs"}); err != nil {
		t.Fatal(err)
	}
	if item, _, _ := eng.BayItem("labs", "b1"); item != "b7" {
		t.Errorf("item = %q, want b7", item)
	}
}

func TestRunRole(t *testing.T) {
	eng := itemFixture(t)
	var out bytes.Buffer
	if err := runRole(eng, &out, []string{"b1", "review"}, "labs", false); err == nil {
		t.Error("role without an item should fail")
	}
	if err := runItem(eng, &out, []string{"b1", "docket"}, itemFlags{dock: "labs"}); err != nil {
		t.Fatal(err)
	}
	if err := runItem(eng, &out, []string{"b2", "docket"}, itemFlags{dock: "labs"}); err == nil {
		t.Fatal("second primary should be refused")
	}
	if err := runItem(eng, &out, []string{"b2", "docket"}, itemFlags{dock: "labs", role: "helper", roleSet: true}); err != nil {
		t.Fatal(err)
	}
	if err := runRole(eng, &out, []string{"b2"}, "labs", false); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "helper" {
		t.Errorf("role read = %q", got)
	}
	if err := runRole(eng, &out, []string{"b2"}, "labs", true); err == nil {
		t.Error("--clear on a second bay would make a second primary")
	}
	if err := runRole(eng, &out, []string{"b2", "x"}, "labs", true); err == nil {
		t.Error("--clear with a role should fail")
	}
}

func TestListRows_CarryItemAndRole(t *testing.T) {
	eng := itemFixture(t)
	var out bytes.Buffer
	if err := runItem(eng, &out, []string{"b1", "docket"}, itemFlags{dock: "labs", role: "helper", roleSet: true}); err != nil {
		t.Fatal(err)
	}
	docks, err := eng.List()
	if err != nil {
		t.Fatal(err)
	}
	rows := ListRows(BuildListView(docks, ListViewOptions{Focus: ListFocus{Kind: FocusAll}, Recursive: true}))
	found := false
	for _, r := range rows {
		if r.BayItem == "docket" && r.BayRole == "helper" {
			found = true
		}
	}
	if !found {
		t.Errorf("no row with item and role: %+v", rows)
	}
}

func TestRunItem_AllListsEveryLinkedBay(t *testing.T) {
	eng := itemFixture(t)
	var out bytes.Buffer
	if err := runItem(eng, &out, []string{"b1", "docket"}, itemFlags{dock: "labs"}); err != nil {
		t.Fatal(err)
	}
	if err := runItem(eng, &out, []string{"b2", "docket"}, itemFlags{dock: "labs", role: "helper", roleSet: true}); err != nil {
		t.Fatal(err)
	}
	if err := eng.BaySetState("labs", "b2", "pr"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runItem(eng, &out, nil, itemFlags{all: true, json: true}); err != nil {
		t.Fatal(err)
	}
	var rows []itemRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	want := []itemRow{{"labs", "b1", "docket", "", ""}, {"labs", "b2", "docket", "helper", "pr"}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %+v, want %+v", rows, want)
	}
	out.Reset()
	if err := runItem(eng, &out, nil, itemFlags{all: true}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "labs/b1\tdocket\nlabs/b2\tdocket\thelper\n" {
		t.Errorf("plain = %q", got)
	}
	for name, f := range map[string]itemFlags{
		"all with clear": {all: true, clr: true},
		"json alone":     {json: true},
	} {
		if err := runItem(eng, &out, nil, f); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
