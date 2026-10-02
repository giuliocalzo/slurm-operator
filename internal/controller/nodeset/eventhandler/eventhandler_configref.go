// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package eventhandler

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	slinkyv1beta1 "github.com/SlinkyProject/slurm-operator/api/v1beta1"
	"github.com/SlinkyProject/slurm-operator/internal/controller/nodeset/indexes"
	"github.com/SlinkyProject/slurm-operator/internal/utils/configrefs"
	"github.com/SlinkyProject/slurm-operator/internal/utils/objectutils"
)

// NewConfigRefEventHandler enqueues the NodeSets that opted in to rolling on
// config change and consume the ConfigMap or Secret of the event.
func NewConfigRefEventHandler(reader client.Reader) *ConfigRefEventHandler {
	return &ConfigRefEventHandler{
		Reader: reader,
	}
}

var _ handler.EventHandler = &ConfigRefEventHandler{}

type ConfigRefEventHandler struct {
	client.Reader
}

func (e *ConfigRefEventHandler) Create(
	ctx context.Context,
	evt event.CreateEvent,
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	e.enqueueRequest(ctx, evt.Object, q)
}

func (e *ConfigRefEventHandler) Update(
	ctx context.Context,
	evt event.UpdateEvent,
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	if !dataChanged(evt.ObjectOld, evt.ObjectNew) {
		return
	}
	e.enqueueRequest(ctx, evt.ObjectNew, q)
}

func (e *ConfigRefEventHandler) Delete(
	ctx context.Context,
	evt event.DeleteEvent,
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	e.enqueueRequest(ctx, evt.Object, q)
}

func (e *ConfigRefEventHandler) Generic(
	ctx context.Context,
	evt event.GenericEvent,
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	// Intentionally blank
}

func (e *ConfigRefEventHandler) enqueueRequest(
	ctx context.Context,
	obj client.Object,
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
) {
	logger := log.FromContext(ctx)

	var ref configrefs.Ref
	switch obj.(type) {
	case *corev1.ConfigMap:
		ref = configrefs.ConfigMap(obj.GetName())
	case *corev1.Secret:
		ref = configrefs.Secret(obj.GetName())
	default:
		return
	}

	nodesetList := &slinkyv1beta1.NodeSetList{}
	opts := []client.ListOption{
		client.InNamespace(obj.GetNamespace()),
		client.MatchingFields{indexes.NodeSetConfigRefsField: ref.String()},
	}
	if err := e.List(ctx, nodesetList, opts...); err != nil {
		logger.Error(err, "failed to list NodeSet CRs", "ref", ref.String())
		return
	}

	for _, nodeset := range nodesetList.Items {
		objectutils.EnqueueRequest(q, &nodeset)
	}
}

func dataChanged(oldObj, newObj client.Object) bool {
	switch o := oldObj.(type) {
	case *corev1.ConfigMap:
		n, ok := newObj.(*corev1.ConfigMap)
		return !ok ||
			!apiequality.Semantic.DeepEqual(o.Data, n.Data) ||
			!apiequality.Semantic.DeepEqual(o.BinaryData, n.BinaryData)
	case *corev1.Secret:
		n, ok := newObj.(*corev1.Secret)
		return !ok || !apiequality.Semantic.DeepEqual(o.Data, n.Data)
	default:
		return true
	}
}
