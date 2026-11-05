package machine

import (
	"errors"
	"regexp"
	"slices"
	"strings"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

// A coding agent's defaults on this machine are what its sessions run with
// when the client starting one names no model: a setting from the owner's
// devices (AdminCommand.set_agent_defaults). They are passed per session;
// the agent's own configuration is never changed.

const kAgentDefaults = "agent_defaults/"

var (
	modelRE  = regexp.MustCompile(`^[A-Za-z0-9._:/\[\]-]{0,100}$`)
	effortRE = regexp.MustCompile(`^[a-z]{0,16}$`)
)

func (n *Machine) loadDefaults() {
	for _, a := range []string{"claude", "codex"} {
		d := &identityv1.AgentDefaults{}
		if ok, _ := n.st.GetProto(kAgentDefaults+a, d); ok {
			n.defaults[a] = d
		}
	}
}

func (n *Machine) setAgentDefaults(d *identityv1.AgentDefaults) error {
	if _, ok := identityv1.AgentSpeed_name[int32(d.Speed)]; !ok {
		return errors.New("unknown speed")
	}
	switch {
	case d.Agent != "claude" && d.Agent != "codex":
		return errors.New("only Claude Code and Codex have defaults")
	case !modelRE.MatchString(d.Model):
		return errors.New("not a model name")
	case !effortRE.MatchString(d.Effort):
		return errors.New("not a reasoning effort")
	case d.Speed != identityv1.AgentSpeed_AGENT_SPEED_UNSPECIFIED && d.Agent != "codex":
		return errors.New("only Codex has a speed")
	}
	d = &identityv1.AgentDefaults{Agent: d.Agent, Model: d.Model, Effort: d.Effort, Speed: d.Speed}
	if err := n.st.SetProto(kAgentDefaults+d.Agent, d); err != nil {
		return err
	}
	n.mu.Lock()
	n.defaults[d.Agent] = d
	n.mu.Unlock()
	return nil
}

// agentDefaults is what agent's sessions run with when the client names no
// model.
func (n *Machine) agentDefaults(agent string) provider.Settings {
	n.mu.Lock()
	defer n.mu.Unlock()
	d := n.defaults[agent]
	return provider.Settings{Model: d.GetModel(), Effort: d.GetEffort(), Speed: d.GetSpeed()}
}

// checkSettings refuses a model or reasoning effort the agent does not
// offer here, naming the ones it does. Only an agent that lists every model
// it takes (Codex) has its model checked; an effort must be one the model
// the session runs offers (runs: "" or unlisted, one any listed model does).
func checkSettings(agent string, i provider.Info, model, effort, runs string) error {
	if model != "" && i.AllModels && !slices.ContainsFunc(i.Models, func(m provider.Model) bool { return m.ID == model }) {
		var ids []string
		for _, m := range i.Models {
			ids = append(ids, m.ID)
		}
		return cerr(connect.CodeInvalidArgument, "%s on this machine has no model %q (one of: %s)", agent, model, strings.Join(ids, ", "))
	}
	if effort == "" {
		return nil
	}
	var efforts []string
	if k := slices.IndexFunc(i.Models, func(m provider.Model) bool { return m.ID == runs }); k >= 0 {
		efforts = i.Models[k].Efforts
	} else {
		for _, m := range i.Models {
			for _, e := range m.Efforts {
				if !slices.Contains(efforts, e) {
					efforts = append(efforts, e)
				}
			}
		}
	}
	switch {
	case len(efforts) == 0:
		return cerr(connect.CodeInvalidArgument, "%s on this machine takes no reasoning effort", agent)
	case !slices.Contains(efforts, effort) && runs != "":
		return cerr(connect.CodeInvalidArgument, "%s's model %s has no reasoning effort %q (one of: %s)", agent, runs, effort, strings.Join(efforts, ", "))
	case !slices.Contains(efforts, effort):
		return cerr(connect.CodeInvalidArgument, "%s has no reasoning effort %q (one of: %s)", agent, effort, strings.Join(efforts, ", "))
	}
	return nil
}

// agentModels fills in what a's status says of models: the ones it offers,
// this machine's defaults and the agent's own configuration. n.mu is held.
func (n *Machine) agentModels(a *machinev1.AgentStatus, i provider.Info) {
	for _, m := range i.Models {
		a.Models = append(a.Models, &machinev1.AgentModel{Id: m.ID, Name: m.Name, Efforts: m.Efforts})
	}
	a.Defaults = n.defaults[a.Name]
	if i.Own != (provider.Settings{}) {
		a.Own = &identityv1.AgentDefaults{Agent: a.Name, Model: i.Own.Model, Effort: i.Own.Effort, Speed: i.Own.Speed}
	}
}
