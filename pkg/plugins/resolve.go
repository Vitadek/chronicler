package plugins

import (
	"fmt"
	"sort"
	"strings"
)

var CoreCapabilities = []string{
	"core:proofreader",
}

type PluginDeps struct {
	ID        string   `json:"id"`
	Provides  []string `json:"provides"`
	Requires  []string `json:"requires"`
	Wants     []string `json:"wants"`
	Conflicts []string `json:"conflicts"`
	Replaces  []string `json:"replaces"`
}

type ResolveInput struct {
	PluginDeps
	Enabled    bool    `json:"enabled"`
	BuildError *string `json:"buildError"`
}

type PluginStatus struct {
	Missing       []string            `json:"missing"`
	UnmetWants    []string            `json:"unmetWants"`
	ConflictsWith []ConflictOwnership `json:"conflictsWith"`
}

type ConflictOwnership struct {
	Capability string `json:"capability"`
	PluginID   string `json:"pluginId"`
}

type Resolution struct {
	Status          map[string]PluginStatus `json:"status"`
	ShadowedCore    []string                `json:"shadowedCore"`
	ActivationOrder []string                `json:"activationOrder"`
	Cycles          map[string]string       `json:"cycles"`
}

func isLive(p ResolveInput) bool {
	return p.Enabled && p.BuildError == nil
}

func providerMap(plugins []ResolveInput) map[string][]string {
	m := make(map[string][]string)
	add := func(cap string, id string) {
		m[cap] = append(m[cap], id)
	}
	for _, p := range plugins {
		add(p.ID, p.ID)
		for _, cap := range p.Provides {
			add(cap, p.ID)
		}
	}
	return m
}

func statusFor(
	plugin ResolveInput,
	others []ResolveInput,
	hostCaps []string,
	coreCaps []string,
) PluginStatus {
	var live []ResolveInput
	for _, o := range others {
		if isLive(o) {
			live = append(live, o)
		}
	}
	providers := providerMap(live)

	has := func(cap string) bool {
		for _, hc := range hostCaps {
			if hc == cap {
				return true
			}
		}
		for _, cc := range coreCaps {
			if cc == cap {
				return true
			}
		}
		_, exists := providers[cap]
		return exists
	}

	seen := make(map[string]ConflictOwnership)
	note := func(capability string, pluginId string) {
		key := capability + " " + pluginId
		seen[key] = ConflictOwnership{Capability: capability, PluginID: pluginId}
	}

	for _, cap := range plugin.Conflicts {
		for _, id := range providers[cap] {
			note(cap, id)
		}
	}

	for _, other := range live {
		for _, cap := range other.Conflicts {
			isConflict := false
			if cap == plugin.ID {
				isConflict = true
			} else {
				for _, pc := range plugin.Provides {
					if pc == cap {
						isConflict = true
						break
					}
				}
			}
			if isConflict {
				note(cap, other.ID)
			}
		}
	}

	conflictsWith := make([]ConflictOwnership, 0)
	for _, c := range seen {
		conflictsWith = append(conflictsWith, c)
	}

	// Deterministic sorting of conflicts
	sort.Slice(conflictsWith, func(i, j int) bool {
		if conflictsWith[i].PluginID != conflictsWith[j].PluginID {
			return conflictsWith[i].PluginID < conflictsWith[j].PluginID
		}
		return conflictsWith[i].Capability < conflictsWith[j].Capability
	})

	missing := make([]string, 0)
	for _, cap := range plugin.Requires {
		if !has(cap) {
			missing = append(missing, cap)
		}
	}

	unmetWants := make([]string, 0)
	for _, cap := range plugin.Wants {
		if !has(cap) {
			unmetWants = append(unmetWants, cap)
		}
	}

	return PluginStatus{
		Missing:       missing,
		UnmetWants:    unmetWants,
		ConflictsWith: conflictsWith,
	}
}

func orderActivation(live []ResolveInput) ([]string, map[string]string) {
	providers := providerMap(live)

	ownersOf := func(p ResolveInput, caps []string) map[string]bool {
		deps := make(map[string]bool)
		for _, cap := range caps {
			for _, owner := range providers[cap] {
				if owner != p.ID {
					deps[owner] = true
				}
			}
		}
		return deps
	}

	hard := make(map[string]map[string]bool)
	soft := make(map[string]map[string]bool)
	for _, p := range live {
		hard[p.ID] = ownersOf(p, p.Requires)
		soft[p.ID] = ownersOf(p, p.Wants)
	}

	order := make([]string, 0)
	pending := make(map[string]bool)
	for _, p := range live {
		pending[p.ID] = true
	}

	drain := func(useSoft bool) bool {
		progressed := false
		again := true
		for again {
			again = false
			var sortedPending []string
			for id := range pending {
				sortedPending = append(sortedPending, id)
			}
			sort.Strings(sortedPending)

			for _, id := range sortedPending {
				hasDeps := false
				for d := range hard[id] {
					if pending[d] {
						hasDeps = true
						break
					}
				}
				if useSoft && !hasDeps {
					for d := range soft[id] {
						if pending[d] {
							hasDeps = true
							break
						}
					}
				}
				if !hasDeps {
					order = append(order, id)
					delete(pending, id)
					progressed = true
					again = true
				}
			}
		}
		return progressed
	}

	drain(true)
	if len(pending) > 0 {
		drain(false)
	}

	cycles := make(map[string]string)
	for id := range pending {
		var stuck []string
		for d := range hard[id] {
			if pending[d] {
				stuck = append(stuck, d)
			}
		}
		sort.Strings(stuck)
		cycles[id] = fmt.Sprintf("Dependency cycle: %s requires %s, which (transitively) requires it back. One of them must drop the dependency.", id, strings.Join(stuck, ", "))
	}

	return order, cycles
}

func Resolve(plugins []ResolveInput, hostCaps []string) Resolution {
	var live []ResolveInput
	for _, p := range plugins {
		if isLive(p) {
			live = append(live, p)
		}
	}

	shadowedCoreMap := make(map[string]bool)
	for _, p := range live {
		for _, r := range p.Replaces {
			shadowedCoreMap[r] = true
		}
	}
	shadowedCore := make([]string, 0)
	for r := range shadowedCoreMap {
		shadowedCore = append(shadowedCore, r)
	}
	sort.Strings(shadowedCore)

	var coreCaps []string
	for _, c := range CoreCapabilities {
		if !shadowedCoreMap[c] {
			coreCaps = append(coreCaps, c)
		}
	}

	status := make(map[string]PluginStatus)
	for _, p := range plugins {
		var others []ResolveInput
		for _, o := range plugins {
			if o.ID != p.ID {
				others = append(others, o)
			}
		}
		status[p.ID] = statusFor(p, others, hostCaps, coreCaps)
	}

	order, cycles := orderActivation(live)
	return Resolution{
		Status:          status,
		ShadowedCore:    shadowedCore,
		ActivationOrder: order,
		Cycles:          cycles,
	}
}

func DependentsOf(id string, plugins []ResolveInput, hostCaps []string) []string {
	before := Resolve(plugins, hostCaps)

	var pluginsWithDisabled []ResolveInput
	for _, p := range plugins {
		if p.ID == id {
			disabledCopy := p
			disabledCopy.Enabled = false
			pluginsWithDisabled = append(pluginsWithDisabled, disabledCopy)
		} else {
			pluginsWithDisabled = append(pluginsWithDisabled, p)
		}
	}

	after := Resolve(pluginsWithDisabled, hostCaps)

	var dependents []string
	for _, p := range plugins {
		if !isLive(p) || p.ID == id {
			continue
		}

		hadMissing := make(map[string]bool)
		for _, m := range before.Status[p.ID].Missing {
			hadMissing[m] = true
		}

		newlyBroken := false
		for _, m := range after.Status[p.ID].Missing {
			if !hadMissing[m] {
				newlyBroken = true
				break
			}
		}

		if newlyBroken {
			dependents = append(dependents, p.ID)
		}
	}

	sort.Strings(dependents)
	return dependents
}
