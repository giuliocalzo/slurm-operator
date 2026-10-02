// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

// Package configrefs finds the ConfigMaps and Secrets a pod consumes and hashes
// their contents, so a controller can roll its pods when that content changes.
package configrefs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/SlinkyProject/slurm-operator/internal/utils/crypto"
)

type Kind string

const (
	KindConfigMap Kind = "ConfigMap"
	KindSecret    Kind = "Secret"
)

// Ref identifies a ConfigMap or Secret in the namespace of the pod consuming it.
type Ref struct {
	Kind Kind
	Name string
}

// String returns "<Kind>/<Name>", the form used as a field index value.
func (r Ref) String() string {
	return string(r.Kind) + "/" + r.Name
}

func ConfigMap(name string) Ref {
	return Ref{Kind: KindConfigMap, Name: name}
}

func Secret(name string) Ref {
	return Ref{Kind: KindSecret, Name: name}
}

// PodSpecRefs returns the ConfigMaps and Secrets referenced by the pod spec's
// configMap, secret, and projected volumes, and by the envFrom and
// env[].valueFrom of its containers and init containers.
func PodSpecRefs(spec *corev1.PodSpec) []Ref {
	var refs []Ref
	for i := range spec.Volumes {
		refs = append(refs, volumeRefs(&spec.Volumes[i])...)
	}
	for i := range spec.InitContainers {
		refs = append(refs, ContainerRefs(&spec.InitContainers[i])...)
	}
	for i := range spec.Containers {
		refs = append(refs, ContainerRefs(&spec.Containers[i])...)
	}
	return Merge(refs)
}

// ContainerRefs returns the ConfigMaps and Secrets referenced by the
// container's envFrom and env[].valueFrom.
func ContainerRefs(container *corev1.Container) []Ref {
	var refs []Ref
	for _, envFrom := range container.EnvFrom {
		if envFrom.ConfigMapRef != nil {
			refs = append(refs, ConfigMap(envFrom.ConfigMapRef.Name))
		}
		if envFrom.SecretRef != nil {
			refs = append(refs, Secret(envFrom.SecretRef.Name))
		}
	}
	for _, env := range container.Env {
		if env.ValueFrom == nil {
			continue
		}
		if env.ValueFrom.ConfigMapKeyRef != nil {
			refs = append(refs, ConfigMap(env.ValueFrom.ConfigMapKeyRef.Name))
		}
		if env.ValueFrom.SecretKeyRef != nil {
			refs = append(refs, Secret(env.ValueFrom.SecretKeyRef.Name))
		}
	}
	return Merge(refs)
}

func volumeRefs(volume *corev1.Volume) []Ref {
	var refs []Ref
	if volume.ConfigMap != nil {
		refs = append(refs, ConfigMap(volume.ConfigMap.Name))
	}
	if volume.Secret != nil {
		refs = append(refs, Secret(volume.Secret.SecretName))
	}
	if volume.Projected != nil {
		for _, source := range volume.Projected.Sources {
			if source.ConfigMap != nil {
				refs = append(refs, ConfigMap(source.ConfigMap.Name))
			}
			if source.Secret != nil {
				refs = append(refs, Secret(source.Secret.Name))
			}
		}
	}
	return refs
}

// Merge combines ref lists into one sorted list without duplicates or refs
// with an empty name.
func Merge(lists ...[]Ref) []Ref {
	out := slices.DeleteFunc(slices.Concat(lists...), func(r Ref) bool {
		return r.Name == ""
	})
	slices.SortFunc(out, func(a, b Ref) int {
		return strings.Compare(a.String(), b.String())
	})
	return slices.Compact(out)
}

// hashEntry is the canonical form of one ref's content. Absent objects are
// recorded explicitly so that creating one later changes the hash.
type hashEntry struct {
	Ref   string            `json:"ref"`
	Found bool              `json:"found"`
	Data  map[string][]byte `json:"data,omitempty"`
}

// Hash returns a checksum over the data of every referenced object in
// namespace. The result changes whenever a key or value of any of them changes,
// or one of them is created or deleted. refs must be sorted, as returned by
// Merge.
func Hash(ctx context.Context, reader client.Reader, namespace string, refs []Ref) (string, error) {
	entries := make([]hashEntry, 0, len(refs))
	for _, ref := range refs {
		data, found, err := getData(ctx, reader, namespace, ref)
		if err != nil {
			return "", err
		}
		entries = append(entries, hashEntry{Ref: ref.String(), Found: found, Data: data})
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return crypto.CheckSum(b), nil
}

func getData(ctx context.Context, reader client.Reader, namespace string, ref Ref) (map[string][]byte, bool, error) {
	key := client.ObjectKey{Namespace: namespace, Name: ref.Name}
	switch ref.Kind {
	case KindConfigMap:
		cm := &corev1.ConfigMap{}
		if err := reader.Get(ctx, key, cm); err != nil {
			return nil, false, ignoreNotFound(err, ref)
		}
		data := make(map[string][]byte, len(cm.Data)+len(cm.BinaryData))
		for k, v := range cm.Data {
			data[k] = []byte(v)
		}
		for k, v := range cm.BinaryData {
			data[k] = v
		}
		return data, true, nil
	case KindSecret:
		secret := &corev1.Secret{}
		if err := reader.Get(ctx, key, secret); err != nil {
			return nil, false, ignoreNotFound(err, ref)
		}
		return secret.Data, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported kind %q", ref.Kind)
	}
}

func ignoreNotFound(err error, ref Ref) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("failed to get %s: %w", ref, err)
}
