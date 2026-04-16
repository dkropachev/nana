package gocli

import "strings"

func runStartWorkIssueReconcile(repoSlug string, publishTarget string, issue startWorkIssueState) (startWorkReconcileResult, error) {
	runID, err := resolveStartWorkIssueRunID(publishTarget, issue)
	if err != nil {
		return startWorkReconcileResult{}, err
	}
	if strings.TrimSpace(runID) == "" {
		return startWorkReconcileResult{
			Status:        startWorkStatusReconciling,
			BlockedReason: defaultString(strings.TrimSpace(issue.LastRunError), "waiting for GitHub work run metadata"),
			ShouldRetry:   true,
		}, nil
	}
	manifestPath, _, err := resolveGithubRunManifestPath(runID, false)
	if err != nil {
		return startWorkReconcileResult{
			Status:        startWorkStatusReconciling,
			BlockedReason: defaultString(strings.TrimSpace(issue.LastRunError), err.Error()),
			RunID:         runID,
			ShouldRetry:   true,
		}, nil
	}
	manifest, err := readGithubWorkManifest(manifestPath)
	if err != nil {
		return startWorkReconcileResult{}, err
	}
	result := startWorkReconcileResult{
		RunID:             runID,
		PublishedPRNumber: manifest.PublishedPRNumber,
		PublishedPRURL:    manifest.PublishedPRURL,
		PublicationState:  defaultString(strings.TrimSpace(manifest.PublicationState), "not_attempted"),
	}
	switch {
	case strings.TrimSpace(issue.LastRunError) != "":
		result.Status = startWorkStatusBlocked
		result.BlockedReason = strings.TrimSpace(issue.LastRunError)
	case !manifest.CreatePROnComplete || manifest.PublishTarget == "local-branch":
		result.Status = startWorkStatusCompleted
	case manifest.NeedsHuman:
		result.Status = startWorkStatusBlocked
		result.BlockedReason = defaultString(strings.TrimSpace(manifest.NeedsHumanReason), "waiting for GitHub feedback")
	case manifest.PublishedPRNumber > 0:
		ciResult, pr, refreshErr := refreshGithubPublicationCIState(manifestPath, &manifest, repoSlug)
		if refreshErr != nil {
			result.Status = startWorkStatusBlocked
			result.BlockedReason = refreshErr.Error()
			result.PublicationState = "blocked"
			return result, nil
		}
		result.PublishedPRNumber = pr.Number
		result.PublishedPRURL = defaultString(strings.TrimSpace(pr.HTMLURL), manifest.PublishedPRURL)
		result.PublicationState = ciResult.State
		switch ciResult.State {
		case "ci_waiting":
			result.Status = startWorkStatusReconciling
			result.BlockedReason = defaultString(strings.TrimSpace(ciResult.Detail), "waiting for publication state")
			result.ShouldRetry = true
		case "blocked":
			result.Status = startWorkStatusBlocked
			result.BlockedReason = defaultString(strings.TrimSpace(ciResult.Detail), "publication blocked")
		default:
			result.Status = startWorkStatusCompleted
		}
	case strings.EqualFold(strings.TrimSpace(manifest.PublicationState), "blocked"):
		result.Status = startWorkStatusBlocked
		result.BlockedReason = defaultString(strings.TrimSpace(manifest.PublicationError), "publication blocked")
	case strings.TrimSpace(manifest.PublicationState) == "" || strings.EqualFold(strings.TrimSpace(manifest.PublicationState), "not_attempted") || strings.EqualFold(strings.TrimSpace(manifest.PublicationState), "ci_waiting"):
		result.Status = startWorkStatusReconciling
		result.BlockedReason = "waiting for publication state"
		result.ShouldRetry = true
	case strings.TrimSpace(manifest.PublicationError) != "":
		result.Status = startWorkStatusBlocked
		result.BlockedReason = strings.TrimSpace(manifest.PublicationError)
	default:
		result.Status = startWorkStatusBlocked
		result.BlockedReason = "run completed without a published PR or terminal publication state"
	}
	return result, nil
}

func resolveStartWorkIssueRunID(publishTarget string, issue startWorkIssueState) (string, error) {
	if strings.TrimSpace(issue.LastRunID) != "" {
		return strings.TrimSpace(issue.LastRunID), nil
	}
	candidates := []string{}
	if publishTarget == "fork" && strings.TrimSpace(issue.ForkURL) != "" {
		candidates = append(candidates, strings.TrimSpace(issue.ForkURL))
	}
	if strings.TrimSpace(issue.SourceURL) != "" {
		candidates = append(candidates, strings.TrimSpace(issue.SourceURL))
	}
	if publishTarget != "fork" && strings.TrimSpace(issue.ForkURL) != "" {
		candidates = append(candidates, strings.TrimSpace(issue.ForkURL))
	}
	for _, candidate := range candidates {
		runID, err := ResolveGithubRunIDForTargetURL(candidate)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(runID) != "" {
			return strings.TrimSpace(runID), nil
		}
	}
	return "", nil
}
