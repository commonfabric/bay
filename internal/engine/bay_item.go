package engine

import (
	"fmt"
	"regexp"
	"time"

	"github.com/commonfabric/bay/internal/manifest"
)

// validItemRe is the rule for item names and roles: lower-case letters,
// digits and hyphens, starting with a letter or digit. It is the rule
// docket applies to item names. Bay does not check that docket has an
// item of the given name.
var validItemRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateItemName checks an item name or a role.
func ValidateItemName(name string) error {
	if !validItemRe.MatchString(name) {
		return fmt.Errorf("invalid name %q: must be lower-case letters, digits and hyphens", name)
	}
	return nil
}

// validateItemRole checks an item and role pair. Both may be empty. A role
// needs an item.
func validateItemRole(item, role string) error {
	if item != "" {
		if err := ValidateItemName(item); err != nil {
			return err
		}
	}
	if role != "" {
		if item == "" {
			return fmt.Errorf("a role needs an item")
		}
		if err := ValidateItemName(role); err != nil {
			return err
		}
	}
	return nil
}

// checkPrimaryFree returns an error when the item already has a primary bay
// (a bay with the item and no role) other than the bay at (exceptDock,
// exceptBayID). It does nothing when role is not empty or item is empty,
// because only a primary bay is limited to one per item.
func checkPrimaryFree(m *manifest.Manifest, item, role, exceptDock, exceptBayID string) error {
	if item == "" || role != "" {
		return nil
	}
	for i := range m.Docks {
		d := &m.Docks[i]
		for j := range d.Bays {
			b := &d.Bays[j]
			if b.Item != item || b.Role != "" {
				continue
			}
			if d.Name == exceptDock && b.ID == exceptBayID {
				continue
			}
			return fmt.Errorf("item %q already has a primary bay (%s:%s); give this bay a role, for example --role helper", item, d.Name, b.ID)
		}
	}
	return nil
}

// BayItem returns a bay's item and role.
func (e *Engine) BayItem(dockName, bayID string) (item, role string, err error) {
	bay, err := e.BayShow(dockName, bayID)
	if err != nil {
		return "", "", err
	}
	return bay.Item, bay.Role, nil
}

// BaySetItem sets a bay's item. When role is non-nil it also sets the role;
// otherwise the role is kept. Setting a different item on a bay that
// already has one needs replace, and then the role is cleared unless one is
// given.
func (e *Engine) BaySetItem(dockName, bayID, item string, role *string, replace bool) error {
	if manifest.IsReservedBayID(bayID) {
		return fmt.Errorf("home is reserved; items are not supported on the home pseudo-bay")
	}
	if item == "" {
		return fmt.Errorf("item name is empty; use --clear to remove the item")
	}
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		newRole := bay.Role
		if bay.Item != "" && bay.Item != item {
			if !replace {
				return fmt.Errorf("bay %s already has item %q; use --replace to change it", bayID, bay.Item)
			}
			newRole = ""
		}
		if role != nil {
			newRole = *role
		}
		if err := validateItemRole(item, newRole); err != nil {
			return err
		}
		if err := checkPrimaryFree(m, item, newRole, dockName, bayID); err != nil {
			return err
		}
		bay.Item = item
		bay.Role = newRole
		bay.LastActive = time.Now().Unix()
		return nil
	})
}

// BayClearItem removes a bay's item and role.
func (e *Engine) BayClearItem(dockName, bayID string) error {
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		bay.Item = ""
		bay.Role = ""
		bay.LastActive = time.Now().Unix()
		return nil
	})
}

// BaySetRole sets a bay's role; an empty role makes it the item's primary
// bay. The bay must have an item.
func (e *Engine) BaySetRole(dockName, bayID, role string) error {
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		if bay.Item == "" {
			return fmt.Errorf("bay %s has no item; set one with `bay item`", bayID)
		}
		if err := validateItemRole(bay.Item, role); err != nil {
			return err
		}
		if err := checkPrimaryFree(m, bay.Item, role, dockName, bayID); err != nil {
			return err
		}
		bay.Role = role
		bay.LastActive = time.Now().Unix()
		return nil
	})
}
