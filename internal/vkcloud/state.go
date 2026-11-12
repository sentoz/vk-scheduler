package vkcloud

import "strings"

func managedK8sState(status string) (string, bool, error) {
	switch status {
	case "RUNNING":
		return "running", false, nil
	case "SHUTOFF":
		return "stopped", false, nil
	case "PROVISIONING", "STARTING", "RECONCILING", "DELETING", "TURNING_ON", "TURNING_OFF":
		return status, true, nil
	case "FAILED":
		return "", false, ErrResourceFailed
	case "DELETED":
		return "", false, ErrResourceNotFound
	default:
		return "", false, ErrUnexpectedState
	}
}

func novaState(status, task string) (string, bool, error) {
	switch status {
	case "ERROR":
		return "", false, ErrResourceFailed
	case "DELETED", "SOFT_DELETED":
		return "", false, ErrResourceNotFound
	}
	switch status {
	case "PAUSED", "SUSPENDED", "SHELVED", "SHELVED_OFFLOADED", "RESCUE":
		return "", false, ErrUnsupportedState
	}
	switch status {
	case "ACTIVE", "SHUTOFF", "BUILD", "REBOOT", "HARD_REBOOT", "REBUILD",
		"RESIZE", "VERIFY_RESIZE", "MIGRATING", "REVERT_RESIZE", "PASSWORD":
	default:
		return "", false, ErrUnexpectedState
	}
	if task != "" {
		return task, true, nil
	}
	switch status {
	case "ACTIVE":
		return "running", false, nil
	case "SHUTOFF":
		return "stopped", false, nil
	default:
		return status, true, nil
	}
}

func magnumState(legacy, current string) (string, bool, error) {
	if strings.HasSuffix(legacy, "_FAILED") || current == "ERROR" {
		return "", false, ErrResourceFailed
	}
	if legacy == "DELETE_COMPLETE" || current == "DELETED" {
		return "", false, ErrResourceNotFound
	}
	if strings.HasSuffix(legacy, "_IN_PROGRESS") {
		return legacy, true, nil
	}
	switch legacy {
	case "TURNING_ON", "TURNING_OFF", "CREATE_IN_PROGRESS", "UPDATE_IN_PROGRESS",
		"UPGRADE_IN_PROGRESS", "DELETE_IN_PROGRESS":
		return legacy, true, nil
	}
	legacyStable := ""
	switch legacy {
	case "TURNED_ON":
		legacyStable = "running"
	case "TURNED_OFF":
		legacyStable = "stopped"
	}
	switch current {
	case "PROVISIONING", "RECONCILING", "DELETING":
		return current, true, nil
	case "RUNNING", "SHUTOFF":
		stable := "running"
		if current == "SHUTOFF" {
			stable = "stopped"
		}
		if legacyStable != "" && legacyStable != stable {
			return "transitioning", true, nil
		}
		return stable, false, nil
	case "":
		if legacyStable != "" {
			return legacyStable, false, nil
		}
		switch legacy {
		case "CREATE_COMPLETE", "UPDATE_COMPLETE", "UPGRADE_COMPLETE":
			return "running", false, nil
		}
	}
	return "", false, ErrUnexpectedState
}
