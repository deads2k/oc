package status

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1client "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/oc/pkg/cli/admin/upgrade/status/mco"
)

type nodePhase uint32

const (
	phaseStateDraining nodePhase = iota
	phaseStateUpdating
	phaseStateRebooting
	phaseStatePaused
	phaseStatePending
	phaseStateUpdated
)

func (phase nodePhase) String() string {
	switch phase {
	case phaseStateDraining:
		return "Draining"
	case phaseStateUpdating:
		return "Updating"
	case phaseStateRebooting:
		return "Rebooting"
	case phaseStatePaused:
		return "Paused"
	case phaseStatePending:
		return "Pending"
	case phaseStateUpdated:
		return "Updated"
	default:
		return ""
	}
}

type nodeAssessment uint32

const (
	nodeAssessmentDegraded nodeAssessment = iota
	nodeAssessmentUnavailable
	nodeAssessmentProgressing
	nodeAssessmentExcluded
	nodeAssessmentOutdated
	nodeAssessmentCompleted
)

func (assessment nodeAssessment) String() string {
	switch assessment {
	case nodeAssessmentDegraded:
		return "Degraded"
	case nodeAssessmentUnavailable:
		return "Unavailable"
	case nodeAssessmentProgressing:
		return "Progressing"
	case nodeAssessmentExcluded:
		return "Excluded"
	case nodeAssessmentOutdated:
		return "Outdated"
	case nodeAssessmentCompleted:
		return "Completed"
	default:
		return ""
	}
}

type nodeDisplayData struct {
	Name          string
	Assessment    nodeAssessment
	Phase         nodePhase
	Version       string
	Estimate      string
	Message       string
	isUnavailable bool
	isDegraded    bool
	isUpdating    bool
	isUpdated     bool
}

type nodesOverviewDisplayData struct {
	Total       int
	Available   int
	Progressing int
	Outdated    int
	Draining    int
	Excluded    int
	Degraded    int
}

type poolDisplayData struct {
	Name          string
	Assessment    assessmentState
	Completion    float64
	Duration      time.Duration
	NodesOverview nodesOverviewDisplayData
	Nodes         []nodeDisplayData
}

func getMachineConfig(ctx context.Context, client mcfgv1client.Interface, machineConfigs []mcfgv1.MachineConfig, machineConfigName string) (*mcfgv1.MachineConfig, error) {
	for _, mc := range machineConfigs {
		if mc.Name == machineConfigName {
			return nil, nil
		}
	}
	return client.MachineconfigurationV1().MachineConfigs().Get(ctx, machineConfigName, v1.GetOptions{})
}

func selectNodesFromPool(pool mcfgv1.MachineConfigPool, allNodes []corev1.Node) ([]corev1.Node, error) {
	var res []corev1.Node
	selector, err := v1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
	if err != nil {
		return nil, err
	}
	for _, node := range allNodes {
		if selector.Matches(labels.Set(node.Labels)) {
			res = append(res, node)
		}
	}
	return res, nil
}

func ellipsizeNames(message string, name string) string {
	if len(name) < 8 {
		return message
	}

	return strings.Replace(message, name, "<node>", -1)
}

func assessNodesStatus(cv *configv1.ClusterVersion, pool mcfgv1.MachineConfigPool, nodes []corev1.Node, machineConfigs []mcfgv1.MachineConfig) ([]nodeDisplayData, []updateInsight) {
	var nodesStatusData []nodeDisplayData
	var insights []updateInsight
	for _, node := range nodes {
		currentVersion, foundCurrent := getOpenShiftVersionOfMachineConfig(machineConfigs, node.Annotations[mco.CurrentMachineConfigAnnotationKey])
		desiredVersion, foundDesired := getOpenShiftVersionOfMachineConfig(machineConfigs, node.Annotations[mco.DesiredMachineConfigAnnotationKey])

		isUnavailable := isNodeUnavailable(node, pool)
		isDegraded := isNodeDegraded(node)
		isUpdated := foundCurrent && isLatestUpdateHistoryVersionEqualTo(cv.Status.History, currentVersion)

		// foundCurrent makes sure we don't blip phase "updating" for nodes that we are not sure
		// of their actual phase, even though the conservative assumption is that the node is
		// at least updating or is updated.
		isUpdating := !isUpdated && foundCurrent && foundDesired && isLatestUpdateHistoryVersionEqualTo(cv.Status.History, desiredVersion)

		phase := calculatePhase(pool, node, isUpdating, isUpdated)
		var estimate string
		var assessment nodeAssessment
		switch phase {
		case phaseStatePaused:
			assessment = nodeAssessmentExcluded
			estimate = "-"
		case phaseStatePending:
			assessment = nodeAssessmentOutdated
			estimate = "?"
		case phaseStateDraining:
			assessment = nodeAssessmentProgressing
			estimate = "+30m"
		case phaseStateUpdating:
			assessment = nodeAssessmentProgressing
			estimate = "+20m"
		case phaseStateRebooting:
			assessment = nodeAssessmentProgressing
			estimate = "+10m"
		case phaseStateUpdated:
			assessment = nodeAssessmentCompleted
			estimate = "-"
		}

		var message string
		if isUnavailable && !isUpdating {
			assessment = nodeAssessmentUnavailable
			message = "Node is unavailable" // TODO: Consider bubbling up the exact reason for --details
			estimate = "?"
			if isUpdated {
				estimate = "-"
			}
		}

		if isDegraded {
			assessment = nodeAssessmentDegraded
			message = node.Annotations[mco.MachineConfigDaemonReasonAnnotationKey]
			estimate = "?"
			if isUpdated {
				estimate = "-"
			}
		}

		insights = append(insights, nodeInsights(pool, node, message, isUnavailable, isUpdating, isDegraded)...)

		nodesStatusData = append(nodesStatusData, nodeDisplayData{
			Name:          node.Name,
			Assessment:    assessment,
			Estimate:      estimate,
			Phase:         phase,
			Message:       ellipsizeNames(message, node.Name),
			Version:       currentVersion,
			isUnavailable: isUnavailable,
			isDegraded:    isDegraded,
			isUpdating:    isUpdating,
			isUpdated:     isUpdated,
		})
	}

	sort.Slice(nodesStatusData, func(i, j int) bool {
		if nodesStatusData[i].Assessment == nodesStatusData[j].Assessment {
			if nodesStatusData[i].Phase == nodesStatusData[j].Phase {
				return nodesStatusData[i].Name < nodesStatusData[j].Name
			}
			return nodesStatusData[i].Phase < nodesStatusData[j].Phase
		}
		return nodesStatusData[i].Assessment < nodesStatusData[j].Assessment
	})

	return nodesStatusData, insights
}

func getOpenShiftVersionOfMachineConfig(machineConfigs []mcfgv1.MachineConfig, name string) (string, bool) {
	for _, mc := range machineConfigs {
		if mc.Name == name {
			openshiftVersion := mc.Annotations[mco.ReleaseImageVersionAnnotationKey]
			return openshiftVersion, openshiftVersion != ""
		}
	}
	return "", false
}

func isNodeDraining(node corev1.Node, isUpdating bool) bool {
	desiredDrain := node.Annotations[mco.DesiredDrainerAnnotationKey]
	appliedDrain := node.Annotations[mco.LastAppliedDrainerAnnotationKey]

	if appliedDrain == "" || desiredDrain == "" {
		return false
	}

	if desiredDrain != appliedDrain {
		desiredVerb := strings.Split(desiredDrain, "-")[0]
		if desiredVerb == mco.DrainerStateDrain {
			return true
		}
	}

	// Node is supposed to be updating but MCD hasn't had the time to update
	// its state from original `Done` to `Working` and start the drain process.
	// Default to drain process so that we don't report completed.
	mcdState := node.Annotations[mco.MachineConfigDaemonStateAnnotationKey]
	return isUpdating && mcdState == mco.MachineConfigDaemonStateDone
}

func isLatestUpdateHistoryVersionEqualTo(history []configv1.UpdateHistory, version string) bool {
	if len(history) > 0 {
		// Check the version of a node against the new entry in the history of CV
		// A paused MCP will not contain the MC of the new history entry
		return history[0].Version == version
	}
	return false
}

func isNodeUnavailable(node corev1.Node, pool mcfgv1.MachineConfigPool) bool {
	lns := mco.NewLayeredNodeState(&node)
	return lns.IsUnavailable(&pool)
}

func isNodeDegraded(node corev1.Node) bool {
	// Inspired by: https://github.com/openshift/machine-config-operator/blob/master/pkg/controller/node/status.go
	if node.Annotations == nil {
		return false
	}
	dconfig, ok := node.Annotations[mco.DesiredMachineConfigAnnotationKey]
	if !ok || dconfig == "" {
		return false
	}
	dstate, ok := node.Annotations[mco.MachineConfigDaemonStateAnnotationKey]
	if !ok || dstate == "" {
		return false
	}

	if dstate == mco.MachineConfigDaemonStateDegraded || dstate == mco.MachineConfigDaemonStateUnreconcilable {
		return true
	}
	return false
}

func calculatePhase(pool mcfgv1.MachineConfigPool, node corev1.Node, isUpdating, isUpdated bool) nodePhase {
	var phase nodePhase
	switch {
	case isUpdating && isNodeDraining(node, isUpdating):
		phase = phaseStateDraining
	case isUpdating:
		phase = mcdUpdatingStateToPhase(node.Annotations[mco.MachineConfigDaemonStateAnnotationKey])
	case isUpdated:
		phase = phaseStateUpdated
	case pool.Spec.Paused:
		phase = phaseStatePaused
	default:
		phase = phaseStatePending
	}
	return phase
}

func mcdUpdatingStateToPhase(state string) nodePhase {
	switch state {
	case mco.MachineConfigDaemonStateWorking:
		return phaseStateUpdating
	case mco.MachineConfigDaemonStateRebooting:
		return phaseStateRebooting
	case mco.MachineConfigDaemonStateDone:
		return phaseStateUpdated
	// The MCD state annotation is not set
	case "":
		return phaseStateUpdating
	default:
		// For other MCD states during an update default to updating
		return phaseStateUpdating
	}
}

func nodeInsights(pool mcfgv1.MachineConfigPool, node corev1.Node, reason string, isUnavailable, isUpdating, isDegraded bool) []updateInsight {
	var insights []updateInsight
	scope := scopeTypeWorkerPool
	if pool.Name == "master" {
		scope = scopeTypeControlPlane
	}
	nodeGvk := node.GroupVersionKind()
	nodeGroupKind := scopeGroupKind{group: nodeGvk.Group, kind: nodeGvk.Kind}
	if isUnavailable && !isUpdating {
		insights = append(insights, updateInsight{
			startedAt: time.Time{},
			scope: updateInsightScope{
				scopeType: scope,
				resources: []scopeResource{{kind: nodeGroupKind, name: node.Name}},
			},
			impact: updateInsightImpact{
				level:       warningImpactLevel,
				impactType:  updateSpeedImpactType,
				summary:     fmt.Sprintf("Node %s is unavailable", node.Name),
				description: reason,
			},
			remediation: updateInsightRemediation{
				reference: "https://docs.openshift.com/container-platform/latest/post_installation_configuration/machine-configuration-tasks.html#understanding-the-machine-config-operator",
			},
		})
	}
	if isDegraded {
		insights = append(insights, updateInsight{
			startedAt: time.Time{},
			scope: updateInsightScope{
				scopeType: scope,
				resources: []scopeResource{{kind: nodeGroupKind, name: node.Name}},
			},
			impact: updateInsightImpact{
				level:       errorImpactLevel,
				impactType:  updateStalledImpactType,
				summary:     fmt.Sprintf("Node %s is degraded", node.Name),
				description: reason,
			},
			remediation: updateInsightRemediation{
				reference: "https://docs.openshift.com/container-platform/latest/post_installation_configuration/machine-configuration-tasks.html#understanding-the-machine-config-operator",
			},
		})
	}
	return insights
}

func assessMachineConfigPool(pool mcfgv1.MachineConfigPool, nodes []nodeDisplayData) (poolDisplayData, []updateInsight) {
	var insights []updateInsight
	poolStatusData := poolDisplayData{
		Name:  pool.Name,
		Nodes: nodes,
		NodesOverview: nodesOverviewDisplayData{
			Total: len(nodes),
		},
	}

	updatedCount := 0
	pendingCount := 0
	for _, node := range nodes {
		if !node.isUnavailable {
			poolStatusData.NodesOverview.Available++
		}
		if node.isDegraded {
			poolStatusData.NodesOverview.Degraded++
		}

		switch node.Phase {
		case phaseStatePaused:
			poolStatusData.NodesOverview.Excluded++
			poolStatusData.NodesOverview.Outdated++
		case phaseStatePending:
			pendingCount++
			poolStatusData.NodesOverview.Outdated++
		case phaseStateDraining:
			poolStatusData.NodesOverview.Draining++
			poolStatusData.NodesOverview.Outdated++
			if !node.isDegraded {
				poolStatusData.NodesOverview.Progressing++
			}
		case phaseStateUpdating:
			poolStatusData.NodesOverview.Outdated++
			if !node.isDegraded {
				poolStatusData.NodesOverview.Progressing++
			}
		case phaseStateRebooting:
			poolStatusData.NodesOverview.Outdated++
			if !node.isDegraded {
				poolStatusData.NodesOverview.Progressing++
			}
		case phaseStateUpdated:
			updatedCount++
		}
	}

	switch {
	case updatedCount == len(nodes):
		poolStatusData.Assessment = assessmentStateCompleted
	case pendingCount == len(nodes):
		poolStatusData.Assessment = assessmentStatePending
	case poolStatusData.NodesOverview.Degraded > 0:
		poolStatusData.Assessment = assessmentStateDegraded
	case poolStatusData.NodesOverview.Excluded > 0:
		poolStatusData.Assessment = assessmentStateExcluded
	default:
		poolStatusData.Assessment = assessmentStateProgressing
	}

	insights = machineConfigPoolInsights(poolStatusData, pool)

	poolStatusData.Completion = float64(updatedCount) / float64(len(nodes)) * 100.0
	return poolStatusData, insights
}

func machineConfigPoolInsights(poolDisplay poolDisplayData, pool mcfgv1.MachineConfigPool) (insights []updateInsight) {
	// TODO: Only generate this insight if the pool has some work remaining that will not finish
	// Depends on how MCO actually works: will it stop updating a node that already started e.g. draining?)
	if poolDisplay.NodesOverview.Excluded > 0 && pool.Spec.Paused {
		poolGvk := pool.GroupVersionKind()
		insights = append(insights, updateInsight{
			startedAt: time.Time{},
			scope: updateInsightScope{
				scopeType: scopeTypeWorkerPool,
				resources: []scopeResource{{kind: scopeGroupKind{group: poolGvk.Group, kind: poolGvk.Kind}, name: pool.Name}},
			},
			impact: updateInsightImpact{
				level:       warningImpactLevel,
				impactType:  updateStalledImpactType,
				summary:     fmt.Sprintf("Outdated nodes in a paused pool '%s' will not be updated", pool.Name),
				description: "Pool is paused, which stops all changes to the nodes in the pool, including updates. The nodes will not be updated until the pool is unpaused by the administrator.",
			},
			remediation: updateInsightRemediation{
				reference: "https://docs.openshift.com/container-platform/latest/support/troubleshooting/troubleshooting-operator-issues.html#troubleshooting-disabling-autoreboot-mco_troubleshooting-operator-issues",
			},
		})
	}
	return insights
}

var workerPoolStatusTemplate = template.Must(template.New("workerPoolStatus").Parse(workerPoolStatusTemplateRaw))

func (pool *poolDisplayData) WritePool(f io.Writer) error {
	return workerPoolStatusTemplate.Execute(f, pool)
}

const workerPoolStatusTemplateRaw = `= Worker Pool =
Worker Pool:     {{ .Name }}
Assessment:      {{ .Assessment }}
Completion:      {{ printf "%.0f" .Completion }}%
Worker Status:   {{ .NodesOverview.Total }} Total, {{ .NodesOverview.Available }} Available, {{ .NodesOverview.Progressing }} Progressing, {{ .NodesOverview.Outdated }} Outdated, {{ .NodesOverview.Draining }} Draining, {{ .NodesOverview.Excluded }} Excluded, {{ .NodesOverview.Degraded }} Degraded
`

func (pool *poolDisplayData) WriteNodes(w io.Writer, detailed bool) {
	if pool.Name == mco.MachineConfigPoolMaster {
		fmt.Fprintf(w, "\nControl Plane Node")
	} else {
		fmt.Fprintf(w, "\nWorker Pool Node")
	}
	if len(pool.Nodes) > 1 {
		fmt.Fprintf(w, "s")
	}

	tabw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = tabw.Write([]byte("\nNAME\tASSESSMENT\tPHASE\tVERSION\tEST\tMESSAGE\n"))
	var total, completed, available, progressing, outdated, draining, excluded int
	for i, node := range pool.Nodes {
		if !detailed && i >= 10 {
			// Limit displaying too many nodes when not in detailed mode
			// Display nodes in undesired states regardless their count
			if !node.isDegraded && (!node.isUnavailable || node.isUpdating) {
				total++
				if node.isUpdated {
					completed++
				} else {
					outdated++
				}
				if !node.isUnavailable {
					available++
				}
				if node.Phase == phaseStateDraining {
					draining++
				}
				if node.Phase == phaseStatePaused {
					excluded++
				}
				if node.Assessment == nodeAssessmentProgressing {
					progressing++
				}
				continue
			}
		}

		version := node.Version
		if version == "" {
			version = "?"
		}

		_, _ = tabw.Write([]byte(node.Name + "\t"))
		_, _ = tabw.Write([]byte(node.Assessment.String() + "\t"))
		_, _ = tabw.Write([]byte(node.Phase.String() + "\t"))
		_, _ = tabw.Write([]byte(version + "\t"))
		_, _ = tabw.Write([]byte(node.Estimate + "\t"))
		_, _ = tabw.Write([]byte(node.Message + "\n"))
	}
	tabw.Flush()
	if total > 0 {
		fmt.Fprintf(w, "...\nOmitted additional %d Total, %d Completed, %d Available, %d Progressing, %d Outdated, %d Draining, %d Excluded, and 0 Degraded nodes.\nPass along --details=nodes to see all information.\n", total, completed, available, progressing, outdated, draining, excluded)
	}
}
