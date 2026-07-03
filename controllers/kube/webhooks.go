package kube

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// The api/v1 types carry ValidateCreate/ValidateUpdate/ValidateDelete and
// Default methods that were written for webhooks but never wired (R18).
// The adapters below register them with the manager's webhook server.

// +kubebuilder:webhook:path=/mutate-rq-davidlangworthy-io-v1-run,mutating=true,failurePolicy=fail,sideEffects=None,groups=rq.davidlangworthy.io,resources=runs,verbs=create;update,versions=v1,name=mrun.rq.davidlangworthy.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-rq-davidlangworthy-io-v1-run,mutating=false,failurePolicy=fail,sideEffects=None,groups=rq.davidlangworthy.io,resources=runs,verbs=create;update,versions=v1,name=vrun.rq.davidlangworthy.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-rq-davidlangworthy-io-v1-budget,mutating=false,failurePolicy=fail,sideEffects=None,groups=rq.davidlangworthy.io,resources=budgets,verbs=create;update,versions=v1,name=vbudget.rq.davidlangworthy.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-rq-davidlangworthy-io-v1-lease,mutating=false,failurePolicy=fail,sideEffects=None,groups=rq.davidlangworthy.io,resources=leases,verbs=create;update,versions=v1,name=vlease.rq.davidlangworthy.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-rq-davidlangworthy-io-v1-reservation,mutating=false,failurePolicy=fail,sideEffects=None,groups=rq.davidlangworthy.io,resources=reservations,verbs=create;update,versions=v1,name=vreservation.rq.davidlangworthy.io,admissionReviewVersions=v1

// SetupWebhooks registers the validating and defaulting webhooks for every
// API type with the manager.
func SetupWebhooks(mgr ctrl.Manager) error {
	if err := ctrl.NewWebhookManagedBy(mgr, &v1.Run{}).
		WithCustomDefaulter(runDefaulter{}).
		WithCustomValidator(legacyValidator{}).
		Complete(); err != nil {
		return fmt.Errorf("run webhook: %w", err)
	}
	if err := ctrl.NewWebhookManagedBy(mgr, &v1.Budget{}).
		WithCustomValidator(legacyValidator{}).
		Complete(); err != nil {
		return fmt.Errorf("budget webhook: %w", err)
	}
	if err := ctrl.NewWebhookManagedBy(mgr, &v1.Lease{}).
		WithCustomValidator(legacyValidator{}).
		Complete(); err != nil {
		return fmt.Errorf("lease webhook: %w", err)
	}
	if err := ctrl.NewWebhookManagedBy(mgr, &v1.Reservation{}).
		WithCustomValidator(legacyValidator{}).
		Complete(); err != nil {
		return fmt.Errorf("reservation webhook: %w", err)
	}
	return nil
}

type runDefaulter struct{}

func (runDefaulter) Default(_ context.Context, obj runtime.Object) error {
	run, ok := obj.(*v1.Run)
	if !ok {
		return fmt.Errorf("expected Run, got %T", obj)
	}
	run.Default()
	return nil
}

// legacyValidator adapts the api/v1 validate methods to the
// admission.CustomValidator interface.
type legacyValidator struct{}

type createValidator interface{ ValidateCreate() error }
type updateValidator interface{ ValidateUpdate(v1.RuntimeObject) error }
type deleteValidator interface{ ValidateDelete() error }

func (legacyValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	validator, ok := obj.(createValidator)
	if !ok {
		return nil, fmt.Errorf("%T has no create validation", obj)
	}
	return nil, validator.ValidateCreate()
}

func (legacyValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	validator, ok := newObj.(updateValidator)
	if !ok {
		return nil, fmt.Errorf("%T has no update validation", newObj)
	}
	return nil, validator.ValidateUpdate(oldObj)
}

func (legacyValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	validator, ok := obj.(deleteValidator)
	if !ok {
		return nil, fmt.Errorf("%T has no delete validation", obj)
	}
	return nil, validator.ValidateDelete()
}
