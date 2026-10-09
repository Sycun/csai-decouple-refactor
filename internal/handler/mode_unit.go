package handler

import (
	"fmt"
	"strings"

	"cyberstrike-ai/internal/agentmode"
	"cyberstrike-ai/internal/plugin"
)

// checkModeUnits validates every mode declaration in a bundle before anything is written.
//
// Same rule as checkPluginUnits: a declaration that cannot be read is a mode that will never
// appear in the catalog, so the whole install is refused rather than half-installed with a
// broken unit nobody recorded. The unit paths were resolved inside the pack directory at
// manifest time, so this reads the exact file the catalog's activation will point at.
func (h *PluginHandler) checkModeUnits(bundle *plugin.Bundle) error {
	var problems []string
	for _, u := range bundle.Units {
		if u.Kind != plugin.KindMode {
			continue
		}
		if _, err := agentmode.ReadDeclaration(u.Path, u.Name); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("能力包的模式单元不可用：%s", strings.Join(problems, "；"))
	}
	return nil
}

// revalidateModeUnit reads the same reviewed surface on the third path into the catalog: the
// enable switch. Install (checkModeUnits) and the boot check (verifyModeUnits) both validate the
// declaration; a unit that was off while its file went bad must not ride the switch past them.
func revalidateModeUnit(u plugin.Unit) error {
	_, err := agentmode.ReadDeclaration(u.Path, u.Name)
	return err
}
