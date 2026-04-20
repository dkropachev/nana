package gocli

import (
	"fmt"
	"strings"
)

type finalReportQualityLintOptions struct {
	RequireRoutingDecision bool
}

type finalReportQualityIssue struct {
	Field   string
	Message string
}

func lintFinalReportQuality(content string, options finalReportQualityLintOptions) []finalReportQualityIssue {
	normalized := strings.ToLower(content)
	checks := []struct {
		field   string
		needles []string
		message string
	}{
		{
			field:   "changed_files",
			needles: []string{"## changed files", "changed files:"},
			message: "final report must identify changed files",
		},
		{
			field:   "verification",
			needles: []string{"## verification evidence", "verification evidence:", "verification:"},
			message: "final report must include verification evidence",
		},
		{
			field:   "simplifications",
			needles: []string{"## simplifications made", "simplifications made:", "simplifications:"},
			message: "final report must state simplifications made or say none were recorded",
		},
		{
			field:   "remaining_risks",
			needles: []string{"## remaining risks", "remaining risks:", "risks:"},
			message: "final report must state remaining risks or say none remain",
		},
	}
	issues := []finalReportQualityIssue{}
	for _, check := range checks {
		if !reportContainsAny(normalized, check.needles) {
			issues = append(issues, finalReportQualityIssue{Field: check.field, Message: check.message})
		}
	}
	if options.RequireRoutingDecision {
		if !strings.Contains(normalized, "routing_decision") {
			issues = append(issues, finalReportQualityIssue{
				Field:   "routing_decision",
				Message: "final report must include routing_decision when runtime routing affected execution",
			})
		} else {
			for _, field := range []string{"mode", "role_tier", "trigger", "confidence"} {
				if !containsRoutingDecisionField(normalized, field) {
					issues = append(issues, finalReportQualityIssue{
						Field:   "routing_decision." + field,
						Message: fmt.Sprintf("routing_decision must include %s", field),
					})
				}
			}
		}
	}
	return issues
}

func reportContainsAny(content string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

func containsRoutingDecisionField(content string, field string) bool {
	return strings.Contains(content, field+":") ||
		strings.Contains(content, field+" =") ||
		strings.Contains(content, field+"=") ||
		strings.Contains(content, `"`+field+`"`)
}

func formatFinalReportQualityIssues(issues []finalReportQualityIssue) string {
	if len(issues) == 0 {
		return ""
	}
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, issue.Field+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}
