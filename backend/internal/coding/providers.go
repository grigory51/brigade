package coding

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

func (r *providerRequest) github(ctx context.Context, repo repository, out *Snapshot) error {
	base := "/repos/" + repo.path
	var runs struct {
		Total int `json:"total_count"`
		Runs  []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			SHA        string `json:"head_sha"`
			Ref        string `json:"head_branch"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			URL        string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	more, err := r.get(ctx, base+"/actions/runs", url.Values{"head_sha": {repo.sha}, "per_page": {strconv.Itoa(maxRuns)}}, &runs)
	if err != nil {
		return err
	}
	if more || runs.Total > maxRuns || len(runs.Runs) > maxRuns {
		out.warn("Only the latest workflow runs are included.")
	}
	for _, item := range runs.Runs[:min(len(runs.Runs), maxRuns)] {
		if item.SHA != repo.sha || item.ID <= 0 {
			out.warn("Provider returned a workflow run for another commit or an invalid ID.")
			continue
		}
		run := Run{ID: strconv.FormatInt(item.ID, 10), Name: item.Name, Ref: item.Ref, Status: item.Status, Conclusion: item.Conclusion, URL: safeURL(item.URL)}
		var jobs struct {
			Total int `json:"total_count"`
			Jobs  []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				URL        string `json:"html_url"`
			} `json:"jobs"`
		}
		more, err := r.get(ctx, base+"/actions/runs/"+run.ID+"/jobs", url.Values{"per_page": {strconv.Itoa(maxJobs)}, "filter": {"latest"}}, &jobs)
		if err != nil {
			run.JobsPartial = true
			out.warn(fmt.Sprintf("Jobs for workflow %s unavailable: %s", run.ID, err))
		} else {
			run.JobsPartial = more || jobs.Total > maxJobs || len(jobs.Jobs) > maxJobs
			if run.JobsPartial {
				out.warn("Workflow jobs are limited to the first page.")
			}
			for _, job := range jobs.Jobs[:min(len(jobs.Jobs), maxJobs)] {
				run.Jobs = append(run.Jobs, Job{ID: strconv.FormatInt(job.ID, 10), Name: job.Name, Status: job.Status, Conclusion: job.Conclusion, URL: safeURL(job.URL)})
			}
		}
		out.Runs = append(out.Runs, run)
	}
	var deployments []struct {
		ID          int64  `json:"id"`
		Environment string `json:"environment"`
		SHA         string `json:"sha"`
		Ref         string `json:"ref"`
	}
	more, err = r.get(ctx, base+"/deployments", url.Values{"sha": {repo.sha}, "per_page": {strconv.Itoa(maxDeployments)}}, &deployments)
	if err != nil {
		out.warn("Deployments unavailable: " + err.Error())
		return nil
	}
	if more || len(deployments) > maxDeployments {
		out.warn("Only the latest deployments are included.")
	}
	for _, item := range deployments[:min(len(deployments), maxDeployments)] {
		if item.SHA != repo.sha || item.ID <= 0 {
			out.warn("Provider returned a deployment for another commit or an invalid ID.")
			continue
		}
		deployment := Deployment{ID: strconv.FormatInt(item.ID, 10), Environment: item.Environment, Ref: item.Ref, CommitSHA: item.SHA}
		var statuses []struct {
			State          string `json:"state"`
			EnvironmentURL string `json:"environment_url"`
			LogURL         string `json:"log_url"`
			TargetURL      string `json:"target_url"`
		}
		_, err := r.get(ctx, base+"/deployments/"+deployment.ID+"/statuses", url.Values{"per_page": {"1"}}, &statuses)
		if err != nil {
			out.warn("Deployment status unavailable: " + err.Error())
		} else if len(statuses) > 0 {
			deployment.Status = statuses[0].State
			deployment.EnvironmentURL = safeURL(statuses[0].EnvironmentURL)
			deployment.URL = safeURL(statuses[0].LogURL)
			if deployment.URL == "" {
				deployment.URL = safeURL(statuses[0].TargetURL)
			}
		}
		out.Deployments = append(out.Deployments, deployment)
	}
	return nil
}

func (r *providerRequest) gitlab(ctx context.Context, repo repository, out *Snapshot) error {
	base := "/projects/" + url.PathEscape(repo.path)
	var pipelines []struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		SHA    string `json:"sha"`
		Ref    string `json:"ref"`
		Status string `json:"status"`
		URL    string `json:"web_url"`
	}
	more, err := r.get(ctx, base+"/pipelines", url.Values{"sha": {repo.sha}, "per_page": {strconv.Itoa(maxRuns)}, "order_by": {"id"}, "sort": {"desc"}}, &pipelines)
	if err != nil {
		return err
	}
	if more || len(pipelines) > maxRuns {
		out.warn("Only the latest pipelines are included.")
	}
	for _, item := range pipelines[:min(len(pipelines), maxRuns)] {
		if item.SHA != repo.sha || item.ID <= 0 {
			out.warn("Provider returned a pipeline for another commit or an invalid ID.")
			continue
		}
		run := Run{ID: strconv.FormatInt(item.ID, 10), Name: item.Name, Status: item.Status, URL: safeURL(item.URL), Ref: item.Ref}
		if run.Name == "" {
			run.Name = "Pipeline #" + run.ID
		}
		var jobs []struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			Status       string `json:"status"`
			URL          string `json:"web_url"`
			Stage        string `json:"stage"`
			AllowFailure bool   `json:"allow_failure"`
		}
		more, err := r.get(ctx, base+"/pipelines/"+run.ID+"/jobs", url.Values{"per_page": {strconv.Itoa(maxJobs)}, "include_retried": {"false"}}, &jobs)
		if err != nil {
			run.JobsPartial = true
			out.warn(fmt.Sprintf("Jobs for pipeline %s unavailable: %s", run.ID, err))
		} else {
			run.JobsPartial = more || len(jobs) > maxJobs
			if run.JobsPartial {
				out.warn("Pipeline jobs are limited to the first page.")
			}
			for _, job := range jobs[:min(len(jobs), maxJobs)] {
				run.Jobs = append(run.Jobs, Job{ID: strconv.FormatInt(job.ID, 10), Name: job.Name, Status: job.Status, URL: safeURL(job.URL), Stage: job.Stage, AllowFailure: job.AllowFailure})
			}
		}
		out.Runs = append(out.Runs, run)
	}
	// GitLab deployments have no SHA filter. Scan a bounded recent window and
	// explicitly report an incomplete search rather than claiming no deployment.
	var deployments []struct {
		ID          int64  `json:"id"`
		SHA         string `json:"sha"`
		Ref         string `json:"ref"`
		Status      string `json:"status"`
		Environment struct {
			Name string `json:"name"`
			URL  string `json:"external_url"`
		} `json:"environment"`
		Deployable struct {
			URL string `json:"web_url"`
		} `json:"deployable"`
	}
	more, err = r.get(ctx, base+"/deployments", url.Values{"per_page": {"50"}, "order_by": {"id"}, "sort": {"desc"}}, &deployments)
	if err != nil {
		out.warn("Deployments unavailable: " + err.Error())
		return nil
	}
	if more || len(deployments) >= 50 {
		out.warn("Deployment search is limited to the 50 most recent project deployments.")
	}
	for _, item := range deployments[:min(len(deployments), 50)] {
		if item.SHA != repo.sha {
			continue
		}
		if len(out.Deployments) == maxDeployments {
			out.warn("Only the latest matching deployments are included.")
			break
		}
		out.Deployments = append(out.Deployments, Deployment{ID: strconv.FormatInt(item.ID, 10), Environment: item.Environment.Name, Status: item.Status, URL: safeURL(item.Deployable.URL), EnvironmentURL: safeURL(item.Environment.URL), Ref: item.Ref, CommitSHA: item.SHA})
	}
	return nil
}
