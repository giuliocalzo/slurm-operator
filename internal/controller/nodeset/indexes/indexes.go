// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package indexes

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
	nodesetutils "github.com/SlinkyProject/slurm-operator/internal/controller/nodeset/utils"
)

type clientIndexer struct {
	obj   client.Object
	field string
	fn    client.IndexerFunc
}

// NodeSetConfigRefsField indexes NodeSets that opted in to rolling on config
// change by the configrefs.Ref.String() of each ConfigMap and Secret they consume.
const NodeSetConfigRefsField = "nodeset.configRefs"

var indexers = []clientIndexer{
	{
		obj:   &corev1.Pod{},
		field: "spec.nodeName",
		fn:    getPodNodeName,
	},
	{
		obj:   &slinkyv1beta1.NodeSet{},
		field: NodeSetConfigRefsField,
		fn:    getNodeSetConfigRefs,
	},
}

func getNodeSetConfigRefs(o client.Object) []string {
	nodeset, ok := o.(*slinkyv1beta1.NodeSet)
	if !ok || !nodesetutils.RolloutOnConfigChange(nodeset) {
		return []string{}
	}
	refs := nodesetutils.ConfigRefs(nodeset)
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.String())
	}
	return out
}

func getPodNodeName(o client.Object) []string {
	obj, ok := o.(runtime.Object)
	if !ok {
		return []string{}
	}
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return []string{}
	}
	return []string{pod.Spec.NodeName}
}

func SetupWithManager(mgr ctrl.Manager) error {
	for _, indexer := range indexers {
		err := mgr.GetFieldIndexer().IndexField(context.Background(), indexer.obj, indexer.field, indexer.fn)
		if err != nil {
			return err
		}
	}
	return nil
}

// NewFakeClientBuilderWithIndexes returns a client builder with the equivalent of addIndexers applied.
func NewFakeClientBuilderWithIndexes(initObjs ...runtime.Object) *fake.ClientBuilder {
	cb := fake.NewClientBuilder().WithRuntimeObjects(initObjs...)
	for _, indexer := range indexers {
		obj := indexer.obj.(runtime.Object)
		cb = cb.WithIndex(obj, indexer.field, indexer.fn)
	}
	return cb
}
