package resolver

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// ActionKind classifies resolver outcomes.
type ActionKind string

const (
	// ActionDropSpare indicates a spare lease was reclaimed.
	ActionDropSpare ActionKind = "DropSpare"
	// ActionShrink indicates a malleable group was shrunk deterministically.
	ActionShrink ActionKind = "Shrink"
	// ActionLottery indicates a lease bundle was ended by the lottery.
	ActionLottery ActionKind = "Lottery"
)

// Input captures the context for resolving an oversubscription.
type Input struct {
	Deficit    int
	Flavor     string
	Scope      map[string]string
	SeedSource string
	Now        time.Time
	Nodes      []topology.SourceNode
	Leases     []*v1.Lease
	Runs       map[string]*v1.Run
}

// Action describes a lease that should be ended.
type Action struct {
	Kind       ActionKind
	Lease      *v1.Lease
	Run        *v1.Run
	GroupIndex string
	GPUs       int
	Reason     string
}

// Result summarises the resolver outcome.
type Result struct {
	Actions []Action
	Seed    string
}

// Resolve executes structural cuts followed by a lottery to clear the deficit.
func Resolve(in Input) (Result, error) {
	if in.Deficit <= 0 {
		return Result{}, nil
	}
	if in.Flavor == "" {
		return Result{}, fmt.Errorf("flavor must be provided")
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}

	nodeIndex := indexNodes(in.Nodes)
	candidates := gatherCandidates(in, nodeIndex)
	deficit := in.Deficit

	var actions []Action

	// 1. Drop spares.
	for _, cand := range candidates.Leases {
		if deficit <= 0 {
			break
		}
		if cand.Lease.Spec.Slice.Role != "Spare" {
			continue
		}
		if cand.Lease.Status.Closed {
			continue
		}
		freed := len(cand.Lease.Spec.Slice.Nodes)
		deficit -= freed
		action := Action{
			Kind:       ActionDropSpare,
			Lease:      cand.Lease,
			Run:        cand.Run,
			GroupIndex: cand.GroupIndex,
			GPUs:       freed,
			Reason:     "DropSpare",
		}
		actions = append(actions, action)
		metrics.IncResolverAction(string(ActionDropSpare))
		cand.Marked = true
	}
	if deficit <= 0 {
		return Result{Actions: actions}, nil
	}

	// 2. Shrink malleable runs.
	shrinkActions, shrinkFreed := shrinkMalleable(deficit, candidates)
	for _, action := range shrinkActions {
		actions = append(actions, action)
		metrics.IncResolverAction(string(ActionShrink))
	}
	deficit -= shrinkFreed
	if deficit <= 0 {
		return Result{Actions: actions}, nil
	}

	// 3. Lottery across remaining groups.
	lotteryActions, seed, err := runLottery(deficit, in, candidates)
	if err != nil {
		return Result{}, err
	}
	for _, action := range lotteryActions {
		actions = append(actions, action)
		metrics.IncResolverAction(string(ActionLottery))
	}
	return Result{Actions: actions, Seed: seed}, nil
}

type leaseCandidate struct {
	Lease      *v1.Lease
	Run        *v1.Run
	GroupIndex string
	GPUs       int
	Marked     bool
}

type runGroup struct {
	Run        *v1.Run
	GroupIndex string
	Leases     []*leaseCandidate
	GPUs       int
	Marked     bool
}

type candidateSet struct {
	Leases []*leaseCandidate
	Groups map[string][]*runGroup // keyed by run key
	Runs   map[string]*runState
}

type runState struct {
	Run        *v1.Run
	TotalGPUs  int
	Remaining  int
	Malleable  *v1.RunMalleability
	GroupByKey map[string]*runGroup
}

type lotteryToken struct {
	runKey string
	group  *runGroup
}

func indexNodes(nodes []topology.SourceNode) map[string]map[string]string {
	idx := make(map[string]map[string]string, len(nodes))
	for _, node := range nodes {
		labels := make(map[string]string, len(node.Labels))
		for k, v := range node.Labels {
			labels[k] = v
		}
		idx[node.Name] = labels
	}
	return idx
}

func gatherCandidates(in Input, nodeIndex map[string]map[string]string) candidateSet {
	result := candidateSet{
		Groups: make(map[string][]*runGroup),
		Runs:   make(map[string]*runState),
	}

	for _, lease := range in.Leases {
		if lease == nil || lease.Status.Closed {
			continue
		}
		runKey := namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		run := in.Runs[runKey]
		if run == nil {
			continue
		}
		if run.Spec.Resources.GPUType != in.Flavor {
			continue
		}
		if !leaseInScope(lease, nodeIndex, in.Scope) {
			continue
		}
		groupIndex := leaseGroupIndex(lease)
		candidate := &leaseCandidate{
			Lease:      lease,
			Run:        run,
			GroupIndex: groupIndex,
			GPUs:       len(lease.Spec.Slice.Nodes),
		}
		result.Leases = append(result.Leases, candidate)

		st, ok := result.Runs[runKey]
		if !ok {
			st = &runState{
				Run:        run,
				Malleable:  run.Spec.Malleable,
				GroupByKey: make(map[string]*runGroup),
			}
			result.Runs[runKey] = st
		}
		st.TotalGPUs += candidate.GPUs
		st.Remaining += candidate.GPUs

		groupKey := groupIndex
		grp, ok := st.GroupByKey[groupKey]
		if !ok {
			grp = &runGroup{Run: run, GroupIndex: groupIndex}
			st.GroupByKey[groupKey] = grp
			result.Groups[runKey] = append(result.Groups[runKey], grp)
		}
		grp.Leases = append(grp.Leases, candidate)
		grp.GPUs += candidate.GPUs
	}
	return result
}

func leaseInScope(lease *v1.Lease, nodeIndex map[string]map[string]string, scope map[string]string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, id := range lease.Spec.Slice.Nodes {
		node := id
		if idx := strings.IndexRune(id, '#'); idx >= 0 {
			node = id[:idx]
		}
		labels := nodeIndex[node]
		if labels == nil {
			return false
		}
		for key, val := range scope {
			if labels[key] != val {
				return false
			}
		}
	}
	return true
}

func leaseGroupIndex(lease *v1.Lease) string {
	if lease.Labels != nil {
		if idx, ok := lease.Labels[binder.LabelGroupIndex]; ok {
			return idx
		}
	}
	return "0"
}

func shrinkMalleable(deficit int, candidates candidateSet) ([]Action, int) {
	if deficit <= 0 {
		return nil, 0
	}
	var actions []Action
	freed := 0

	type shrinkCandidate struct {
		runKey string
		group  *runGroup
	}

	var shrinkList []shrinkCandidate
	for runKey, st := range candidates.Runs {
		if st.Malleable == nil {
			continue
		}
		for _, grp := range candidates.Groups[runKey] {
			shrinkList = append(shrinkList, shrinkCandidate{runKey: runKey, group: grp})
		}
	}

	sort.Slice(shrinkList, func(i, j int) bool {
		if shrinkList[i].runKey == shrinkList[j].runKey {
			return shrinkList[i].group.GroupIndex > shrinkList[j].group.GroupIndex
		}
		return shrinkList[i].runKey < shrinkList[j].runKey
	})

	for _, item := range shrinkList {
		if deficit <= 0 {
			break
		}
		st := candidates.Runs[item.runKey]
		if st == nil || st.Malleable == nil {
			continue
		}
		grp := item.group
		if grp.Marked {
			continue
		}
		if st.Remaining-grp.GPUs < int(st.Malleable.MinTotalGPUs) {
			continue
		}
		actions = append(actions, buildActions(ActionShrink, "Shrink", grp)...)
		grp.Marked = true
		st.Remaining -= grp.GPUs
		freed += grp.GPUs
		deficit -= grp.GPUs
	}

	return actions, freed
}

func buildActions(kind ActionKind, reason string, grp *runGroup) []Action {
	actions := make([]Action, 0, len(grp.Leases))
	for _, lease := range grp.Leases {
		lease.Marked = true
		actions = append(actions, Action{
			Kind:       kind,
			Lease:      lease.Lease,
			Run:        lease.Run,
			GroupIndex: grp.GroupIndex,
			GPUs:       lease.GPUs,
			Reason:     reason,
		})
	}
	return actions
}

func runLottery(deficit int, in Input, candidates candidateSet) ([]Action, string, error) {
	if deficit <= 0 {
		return nil, "", nil
	}

	seed := computeSeed(in.SeedSource, in.Now)
	rng := rand.New(rand.NewSource(seedValue(seed)))

	type token struct {
		runKey string
		group  *runGroup
	}

	tokensByOwner := make(map[string][]lotteryToken)
	owners := make([]string, 0)

	for runKey, st := range candidates.Runs {
		owner := st.Run.Spec.Owner
		available := false
		for _, grp := range candidates.Groups[runKey] {
			if grp.Marked {
				continue
			}
			if st.Malleable != nil && st.Remaining-grp.GPUs < int(st.Malleable.MinTotalGPUs) {
				continue
			}
			tokensByOwner[owner] = append(tokensByOwner[owner], lotteryToken{runKey: runKey, group: grp})
			available = true
		}
		if available {
			owners = append(owners, owner)
		}
	}

	if len(tokensByOwner) == 0 {
		return nil, "", fmt.Errorf("no candidates available for lottery")
	}

	owners = uniqueStrings(owners)
	sort.Strings(owners)

	var actions []Action
	for deficit > 0 {
		if len(owners) == 0 {
			return nil, "", fmt.Errorf("lottery exhausted before clearing deficit")
		}
		ownerIdx := rng.Intn(len(owners))
		owner := owners[ownerIdx]
		tokens := tokensByOwner[owner]
		if len(tokens) == 0 {
			owners = append(owners[:ownerIdx], owners[ownerIdx+1:]...)
			continue
		}
		tokenIdx := rng.Intn(len(tokens))
		tok := tokens[tokenIdx]
		grp := tok.group
		if grp.Marked {
			tokensByOwner[owner] = removeToken(tokens, tokenIdx)
			if len(tokensByOwner[owner]) == 0 {
				owners = append(owners[:ownerIdx], owners[ownerIdx+1:]...)
			}
			continue
		}
		runState := candidates.Runs[tok.runKey]
		if runState == nil {
			tokensByOwner[owner] = removeToken(tokens, tokenIdx)
			continue
		}
		if runState.Malleable != nil && runState.Remaining-grp.GPUs < int(runState.Malleable.MinTotalGPUs) {
			tokensByOwner[owner] = removeToken(tokens, tokenIdx)
			if len(tokensByOwner[owner]) == 0 {
				owners = append(owners[:ownerIdx], owners[ownerIdx+1:]...)
			}
			continue
		}

		grp.Marked = true
		runState.Remaining -= grp.GPUs
		actions = append(actions, buildActions(ActionLottery, fmt.Sprintf("RandomPreempt(%s)", seed), grp)...)
		deficit -= grp.GPUs

		tokensByOwner[owner] = removeToken(tokens, tokenIdx)
		if len(tokensByOwner[owner]) == 0 {
			owners = append(owners[:ownerIdx], owners[ownerIdx+1:]...)
		}
	}

	return actions, seed, nil
}

func computeSeed(source string, now time.Time) string {
	payload := fmt.Sprintf("%s|%d", source, now.UnixNano())
	digest := sha256.Sum256([]byte(payload))
	return "0x" + hex.EncodeToString(digest[:8])
}

func seedValue(seed string) int64 {
	if len(seed) <= 2 {
		return time.Now().UnixNano()
	}
	raw, err := hex.DecodeString(seed[2:])
	if err != nil || len(raw) < 8 {
		return time.Now().UnixNano()
	}
	return int64(binary.BigEndian.Uint64(raw[:8]))
}

func removeToken(tokens []lotteryToken, idx int) []lotteryToken {
	if idx < 0 || idx >= len(tokens) {
		return tokens
	}
	tokens[idx] = tokens[len(tokens)-1]
	return tokens[:len(tokens)-1]
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	var prev string
	first := true
	for _, val := range values {
		if first || val != prev {
			out = append(out, val)
			prev = val
			first = false
		}
	}
	return out
}

func namespacedKey(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}
