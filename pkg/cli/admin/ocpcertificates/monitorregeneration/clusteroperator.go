package monitorregeneration

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (o *MonitorCertificatesRuntime) createClusterOperator(obj interface{}, isFirstSync bool) {
	clusterOperator, ok := obj.(*configv1.ClusterOperator)
	if !ok {
		fmt.Fprint(o.IOStreams.ErrOut, "unexpected create obj %T", obj)
	}

	if oldObj, _ := o.interestingClusterOperators.get(clusterOperator.Name); oldObj != nil {
		o.updateClusterOperator(obj, oldObj)
		return
	}

	o.interestingClusterOperators.upsert(clusterOperator.Name, clusterOperator)
}

func (o *MonitorCertificatesRuntime) updateClusterOperator(obj, oldObj interface{}) {
	clusterOperator, ok := obj.(*configv1.ClusterOperator)
	if !ok {
		fmt.Fprint(o.IOStreams.ErrOut, "unexpected update obj %T", obj)
	}
	defer o.interestingClusterOperators.upsert(clusterOperator.Name, clusterOperator)

	oldClusterOperator, ok := oldObj.(*configv1.ClusterOperator)
	if !ok {
		fmt.Fprint(o.IOStreams.ErrOut, "unexpected update oldObj %T", oldObj)
	}

	o.handleClusterOperator(clusterOperator, oldClusterOperator)
}

func defaultCondition(conditionType configv1.ClusterStatusConditionType) *configv1.ClusterOperatorStatusCondition {
	return &configv1.ClusterOperatorStatusCondition{
		Type:               conditionType,
		Status:             configv1.ConditionUnknown,
		LastTransitionTime: metav1.Now(),
		Reason:             "Missing",
		Message:            "Missing",
	}
}

func FindStatusConditionOrSynthetic(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) *configv1.ClusterOperatorStatusCondition {
	ret := v1helpers.FindStatusCondition(conditions, conditionType)
	if ret != nil {
		return ret
	}

	return defaultCondition(conditionType)
}

func meaningfulChange(condition, oldCondition *configv1.ClusterOperatorStatusCondition) bool {
	if condition.Status != oldCondition.Status {
		return true
	}
	if condition.Reason != oldCondition.Reason {
		return true
	}
	if messageMinusConflicts(condition.Message) != messageMinusConflicts(oldCondition.Message) {
		return true
	}

	return false
}

func messageMinusConflicts(message string) string {
	nonConflictLines := []string{}
	scanner := bufio.NewScanner(bytes.NewBufferString(message))
	for scanner.Scan() {
		currLine := scanner.Text()
		if strings.Contains(currLine, "the object has been modified") {
			continue
		}
		nonConflictLines = append(nonConflictLines, currLine)
	}

	return strings.Join(nonConflictLines, "\n")
}

func availableStatusString(status configv1.ConditionStatus) string {
	switch {
	case status == configv1.ConditionFalse:
		return "Unavailable"
	case status == configv1.ConditionTrue:
		return "Available"
	default:
		return fmt.Sprintf("Available=%v", status)
	}
}

func degradedStatusString(status configv1.ConditionStatus) string {
	switch {
	case status == configv1.ConditionFalse:
		return "NotDegraded"
	case status == configv1.ConditionTrue:
		return "Degraded"
	default:
		return fmt.Sprintf("Degraded=%v", status)
	}
}
func progressingStatusString(status configv1.ConditionStatus) string {
	switch {
	case status == configv1.ConditionFalse:
		return "NotProgressing"
	case status == configv1.ConditionTrue:
		return "Progressing"
	default:
		return fmt.Sprintf("Progressing=%v", status)
	}
}

func (o *MonitorCertificatesRuntime) describeConditionChange(clusterOperatorName string, newCondition, oldCondition *configv1.ClusterOperatorStatusCondition, conditionDescription string) {
	if meaningfulChange(newCondition, oldCondition) {
		switch {
		case newCondition.Status != oldCondition.Status:
			fmt.Fprintf(o.IOStreams.Out, "clusteroperators/%v -- %v - %v - %v\n", clusterOperatorName, conditionDescription, newCondition.Reason, strings.ReplaceAll(newCondition.Message, "\n", "\n   "))
		case newCondition.Reason != oldCondition.Reason:
			fmt.Fprintf(o.IOStreams.Out, "clusteroperators/%v -- Still %v, new reason - %v - %v\n", clusterOperatorName, conditionDescription, newCondition.Reason, strings.ReplaceAll(newCondition.Message, "\n", "\n   "))
		case messageMinusConflicts(newCondition.Message) != messageMinusConflicts(oldCondition.Message):
			fmt.Fprintf(o.IOStreams.Out, "clusteroperators/%v -- Still %v, new message - %v - %v\n", clusterOperatorName, conditionDescription, newCondition.Reason, strings.ReplaceAll(messageMinusConflicts(newCondition.Message), "\n", "\n   "))
		}
	}
}

func (o *MonitorCertificatesRuntime) describeOperator(clusterOperatorName string, newCondition, oldCondition *configv1.ClusterOperatorStatusCondition, conditionDescription string) {
	if meaningfulChange(newCondition, oldCondition) {
		switch {
		case newCondition.Status != oldCondition.Status:
			fmt.Fprintf(o.IOStreams.Out, "clusteroperators/%v -- %v - %v - %v\n", clusterOperatorName, conditionDescription, newCondition.Reason, strings.ReplaceAll(newCondition.Message, "\n", "\n   "))
		case newCondition.Reason != oldCondition.Reason:
			fmt.Fprintf(o.IOStreams.Out, "clusteroperators/%v -- Still %v, new reason - %v - %v\n", clusterOperatorName, conditionDescription, newCondition.Reason, strings.ReplaceAll(newCondition.Message, "\n", "\n   "))
		case messageMinusConflicts(newCondition.Message) != messageMinusConflicts(oldCondition.Message):
			fmt.Fprintf(o.IOStreams.Out, "clusteroperators/%v -- Still %v, new message - %v - %v\n", clusterOperatorName, conditionDescription, newCondition.Reason, strings.ReplaceAll(messageMinusConflicts(newCondition.Message), "\n", "\n   "))
		}
	}
}

func (o *MonitorCertificatesRuntime) handleClusterOperator(clusterOperator, oldClusterOperator *configv1.ClusterOperator) {
	oldAvailable := FindStatusConditionOrSynthetic(oldClusterOperator.Status.Conditions, configv1.OperatorAvailable)
	newAvailable := FindStatusConditionOrSynthetic(clusterOperator.Status.Conditions, configv1.OperatorAvailable)
	oldDegraded := FindStatusConditionOrSynthetic(oldClusterOperator.Status.Conditions, configv1.OperatorDegraded)
	newDegraded := FindStatusConditionOrSynthetic(clusterOperator.Status.Conditions, configv1.OperatorDegraded)
	oldProgressing := FindStatusConditionOrSynthetic(oldClusterOperator.Status.Conditions, configv1.OperatorProgressing)
	newProgressing := FindStatusConditionOrSynthetic(clusterOperator.Status.Conditions, configv1.OperatorProgressing)

	availableChanged := meaningfulChange(newAvailable, oldAvailable)
	degradedChanged := meaningfulChange(newDegraded, oldDegraded)
	progressingChanged := meaningfulChange(newProgressing, oldProgressing)
	meaningfulStatusChange := availableChanged || degradedChanged || progressingChanged
	if !meaningfulStatusChange {
		return
	}

	statusLines := []string{}
	headerLine := ""

	oldIsStable := oldAvailable.Status == configv1.ConditionTrue && oldDegraded.Status == configv1.ConditionFalse && oldProgressing.Status == configv1.ConditionFalse
	newIsStable := newAvailable.Status == configv1.ConditionTrue && newDegraded.Status == configv1.ConditionFalse && newProgressing.Status == configv1.ConditionFalse
	switch {
	case oldIsStable && !newIsStable:
		headerLine = fmt.Sprintf("clusteroperators/%v -- Destabilized", clusterOperator.Name)
	case oldIsStable && newIsStable:
		headerLine = fmt.Sprintf("clusteroperators/%v -- Stable", clusterOperator.Name)
	case !oldIsStable && newIsStable:
		headerLine = fmt.Sprintf("clusteroperators/%v -- Stabilized", clusterOperator.Name)
	case !oldIsStable && !newIsStable:
		headerLine = fmt.Sprintf("clusteroperators/%v -- Unstable", clusterOperator.Name)
	}

	if newAvailable.Status == configv1.ConditionTrue {
		statusLines = append(statusLines, fmt.Sprintf("Available - %v", newAvailable.Reason))
	} else {
		statusLines = append(statusLines, fmt.Sprintf("Unavailable - %v", newAvailable.Reason))
	}
	if availableChanged {
		lines := strings.Split(messageMinusConflicts(newAvailable.Message), "\n")
		for _, line := range lines {
			statusLines = append(statusLines, "    "+line)
		}
	}
	if newDegraded.Status != configv1.ConditionTrue {
		statusLines = append(statusLines, fmt.Sprintf("Not Degraded - %v", newDegraded.Reason))
	} else {
		statusLines = append(statusLines, fmt.Sprintf("Degraded - %v", newDegraded.Reason))
	}
	if degradedChanged {
		lines := strings.Split(messageMinusConflicts(newDegraded.Message), "\n")
		for _, line := range lines {
			statusLines = append(statusLines, "    "+line)
		}
	}
	if newProgressing.Status != configv1.ConditionTrue {
		statusLines = append(statusLines, fmt.Sprintf("Not Progressing - %v", newProgressing.Reason))
	} else {
		statusLines = append(statusLines, fmt.Sprintf("Progressing - %v", newProgressing.Reason))
	}
	if progressingChanged {
		lines := strings.Split(messageMinusConflicts(newProgressing.Message), "\n")
		for _, line := range lines {
			statusLines = append(statusLines, "    "+line)
		}
	}

	statusMessage := strings.Join(statusLines, "\n    ")
	fmt.Fprintf(o.IOStreams.Out, "%v\n    %v\n", headerLine, statusMessage)
}

func (o *MonitorCertificatesRuntime) deleteClusterOperator(obj interface{}) {
	clusterOperator, ok := obj.(*configv1.ClusterOperator)
	if !ok {
		fmt.Fprint(o.IOStreams.ErrOut, "unexpected create obj %T", obj)
	}

	o.interestingClusterOperators.remove(clusterOperator.Name)
}
