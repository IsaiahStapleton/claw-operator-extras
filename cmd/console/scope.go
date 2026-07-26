/*
Copyright 2026 Red Hat.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Which Claws may this user see? The answer comes from the API server under
// the user's own identity — the console lists Claws impersonated, so a user
// only ever learns about namespaces they already have access to.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
)

// ClawRef identifies one Claw instance the user can read.
type ClawRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Pod       string `json:"pod"`
	Ready     bool   `json:"ready"`
}

// clawList is the shape this console needs out of a Claw list response.
type clawList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// listClaws returns every Claw the impersonated user can see. A cluster-wide
// list is tried first because it is one request, but most users hold only
// namespace-scoped RBAC and are refused it — so a 403 falls back to listing
// each namespace they can see. A user with access to nothing gets an empty
// list, not an error: "you can see nothing" is an answer, not a failure.
func (s *server) listClaws(ctx context.Context, identity userIdentity) ([]ClawRef, error) {
	var list clawList
	err := s.kubeGet(ctx, identity, fmt.Sprintf("/apis/%s/%s/claws", clawAPIGroup, clawAPIVersion), &list)
	if err == nil {
		return clawRefs(list), nil
	}
	if statusCodeFor(err) != http.StatusForbidden {
		return nil, err
	}

	namespaces, err := s.visibleNamespaces(ctx, identity)
	if err != nil {
		return nil, err
	}
	// Empty, not nil: this marshals to [] so a client can treat "no access" as
	// an ordinary empty list rather than a missing field.
	out := []ClawRef{}
	for _, ns := range namespaces {
		var nsList clawList
		path := fmt.Sprintf("/apis/%s/%s/namespaces/%s/claws",
			clawAPIGroup, clawAPIVersion, url.PathEscape(ns))
		if err := s.kubeGet(ctx, identity, path, &nsList); err != nil {
			// A namespace the user can see but whose Claws they cannot list is
			// simply not shown; one such namespace must not hide the rest.
			continue
		}
		out = append(out, clawRefs(nsList)...)
	}
	sortClawRefs(out)
	return out, nil
}

// visibleNamespaces lists what the user can see, preferring the namespaces API
// and falling back to OpenShift projects, which is scoped to the caller and so
// succeeds where a cluster-wide namespace list is refused.
func (s *server) visibleNamespaces(ctx context.Context, identity userIdentity) ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	err := s.kubeGet(ctx, identity, "/api/v1/namespaces", &list)
	if err != nil {
		if statusCodeFor(err) != http.StatusForbidden {
			return nil, err
		}
		if err := s.kubeGet(ctx, identity, "/apis/project.openshift.io/v1/projects", &list); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.Name != "" {
			names = append(names, item.Metadata.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func clawRefs(list clawList) []ClawRef {
	out := make([]ClawRef, 0, len(list.Items))
	for _, item := range list.Items {
		ready := false
		for _, c := range item.Status.Conditions {
			if c.Type == "Ready" {
				ready = c.Status == "True"
			}
		}
		out = append(out, ClawRef{
			Namespace: item.Metadata.Namespace,
			Name:      item.Metadata.Name,
			Ready:     ready,
		})
	}
	sortClawRefs(out)
	return out
}

func sortClawRefs(out []ClawRef) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
}

// clawPod resolves the running pod backing a Claw. The operator labels the
// workload with the instance name, which is what makes this a lookup rather
// than a guess at the deployment's generated pod name.
func (s *server) clawPod(ctx context.Context, identity userIdentity, namespace, claw string) (string, error) {
	selector := url.QueryEscape("app=claw," + clawInstanceLabel + "=" + claw)
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s",
		url.PathEscape(namespace), selector)

	var list struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				DeletionTimestamp string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := s.kubeGet(ctx, identity, path, &list); err != nil {
		return "", err
	}

	// Prefer a pod that is actually Ready; a terminating or pending pod cannot
	// serve an exec, and reporting "no pod" is better than a confusing timeout.
	fallback := ""
	for _, item := range list.Items {
		if item.Metadata.DeletionTimestamp != "" || item.Status.Phase != "Running" {
			continue
		}
		for _, c := range item.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				return item.Metadata.Name, nil
			}
		}
		if fallback == "" {
			fallback = item.Metadata.Name
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", apiError{StatusCode: 404,
		Message: fmt.Sprintf("no running pod found for Claw %s/%s", namespace, claw)}
}

const clawInstanceLabel = clawAPIGroup + "/instance"
