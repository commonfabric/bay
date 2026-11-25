package cli

import (
	"fmt"

	"github.com/commonfabric/bay/internal/manifest"
	"github.com/spf13/cobra"
)

// resolveItemTarget resolves the optional bay argument of `bay item` and
// `bay role` and rejects the home pseudo-bay.
func resolveItemTarget(target, dockFlag string) (dock, bayID string, err error) {
	eng, err := newEngine()
	if err != nil {
		return "", "", err
	}
	dock, bayID, err = resolveBayArg(eng, target, dockFlag)
	if err != nil {
		return "", "", err
	}
	if manifest.IsReservedBayID(bayID) {
		return "", "", fmt.Errorf("home is reserved; items are not supported on the home pseudo-bay")
	}
	return dock, bayID, nil
}

func newBayItemCmd() *cobra.Command {
	var dockFlag, role string
	var clear, replace bool

	cmd := &cobra.Command{
		Use:   "item [id] [<name>]",
		Short: "Read or set the docket item a bay works on",
		Long: `Read or set the name of the docket item a bay works on. The name uses
lower-case letters, digits and hyphens. Bay does not check that docket has
such an item.

An item has at most one primary bay: a bay with the item and no role.
Another bay of the item needs a role (--role).

  bay item                       print the current bay's item
  bay item b3                    print bay b3's item
  bay item docket                set the current bay's item
  bay item b3 docket --role review   set by bay ID, with a role
  bay item b3 other --replace    change an item the bay already has
  bay item --clear               remove the item and role`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, name, haveName, err := splitItemArgs(args)
			if err != nil {
				return err
			}
			dock, bayID, err := resolveItemTarget(target, dockFlag)
			if err != nil {
				return err
			}
			eng, err := newEngine()
			if err != nil {
				return err
			}
			switch {
			case clear:
				if haveName || cmd.Flags().Changed("role") {
					return fmt.Errorf("--clear takes no name or --role")
				}
				return eng.BayClearItem(dock, bayID)
			case haveName:
				var rolePtr *string
				if cmd.Flags().Changed("role") {
					rolePtr = &role
				}
				return eng.BaySetItem(dock, bayID, name, rolePtr, replace)
			default:
				if cmd.Flags().Changed("role") || replace {
					return fmt.Errorf("--role and --replace need an item name")
				}
				item, _, err := eng.BayItem(dock, bayID)
				if err != nil {
					return err
				}
				if item != "" {
					fmt.Println(item)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")
	cmd.Flags().StringVar(&role, "role", "", "role of the bay within the item (empty = primary)")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the item and role")
	cmd.Flags().BoolVar(&replace, "replace", false, "allow changing an item the bay already has")
	return cmd
}

// splitItemArgs separates the optional bay argument from the optional item
// name or role. A single argument that looks like a bay ID (such as b3)
// is the bay, and the command reads; any other single argument is the
// name to set on the current bay. To set a name that looks like a bay ID,
// pass the bay too: `bay item self b3`.
func splitItemArgs(args []string) (target, name string, haveName bool, err error) {
	switch len(args) {
	case 0:
		return "self", "", false, nil
	case 1:
		if manifest.IsBayID(args[0]) {
			return args[0], "", false, nil
		}
		return "self", args[0], true, nil
	default:
		return args[0], args[1], true, nil
	}
}

func newBayRoleCmd() *cobra.Command {
	var dockFlag string
	var clear bool

	cmd := &cobra.Command{
		Use:   "role [id] [<role>]",
		Short: "Read or set a bay's role within its docket item",
		Long: `Read or set the role of a bay within its item, such as "review" or
"helper". The primary bay of an item has no role. The bay needs an item
first (see bay item).

  bay role                  print the current bay's role
  bay role b3               print bay b3's role
  bay role review           set the current bay's role
  bay role b3 review        set by bay ID
  bay role --clear          make the bay the item's primary bay`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, role, haveRole, err := splitItemArgs(args)
			if err != nil {
				return err
			}
			dock, bayID, err := resolveItemTarget(target, dockFlag)
			if err != nil {
				return err
			}
			eng, err := newEngine()
			if err != nil {
				return err
			}
			if clear {
				if haveRole {
					return fmt.Errorf("--clear takes no role")
				}
				return eng.BaySetRole(dock, bayID, "")
			}
			if haveRole {
				return eng.BaySetRole(dock, bayID, role)
			}
			_, r, err := eng.BayItem(dock, bayID)
			if err != nil {
				return err
			}
			if r != "" {
				fmt.Println(r)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the role, making the bay the item's primary bay")
	return cmd
}
