package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/asset"
	"github.com/freefsm-project/freefsm/internal/ent/customercontact"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/project"
	"github.com/freefsm-project/freefsm/internal/ent/taglink"
)

type DependencyService struct {
	client *ent.Client
}

func NewDependencyService(client *ent.Client) *DependencyService {
	return &DependencyService{client: client}
}

func (s *DependencyService) CanDeleteCustomer(ctx context.Context, id int64) (bool, string) {
	var parts []string
	if n, _ := s.client.Job.Query().Where(job.CustomerID(id), job.DeletedAtIsNil()).Count(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("%d job(s)", n))
	}
	if n, _ := s.client.Project.Query().Where(project.CustomerID(id), project.DeletedAtIsNil()).Count(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("%d project(s)", n))
	}
	if n, _ := s.client.Estimate.Query().Where(estimate.CustomerID(id)).Count(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("%d estimate(s)", n))
	}
	if n, _ := s.client.Invoice.Query().Where(invoice.CustomerID(id)).Count(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("%d invoice(s)", n))
	}
	if n, _ := s.client.Asset.Query().Where(asset.CustomerID(id), asset.DeletedAtIsNil()).Count(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("%d asset(s)", n))
	}
	if n, _ := s.client.CustomerContact.Query().Where(customercontact.CustomerID(id)).Count(ctx); n > 0 {
		parts = append(parts, fmt.Sprintf("%d contact(s)", n))
	}
	if len(parts) > 0 {
		msg := "Cannot delete customer — it has "
		for i, p := range parts {
			if i > 0 {
				if i == len(parts)-1 {
					msg += " and "
				} else {
					msg += ", "
				}
			}
			msg += p
		}
		return false, msg
	}
	return true, ""
}

func (s *DependencyService) CanDeleteJob(ctx context.Context, id int64) (bool, string) {
	if n, _ := s.client.Estimate.Query().Where(estimate.JobID(id)).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete job — it has %d linked estimate(s)", n)
	}
	if n, _ := s.client.Invoice.Query().Where(invoice.JobID(id)).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete job — it has %d linked invoice(s)", n)
	}
	return true, ""
}

func (s *DependencyService) CanDeleteProject(ctx context.Context, id int64) (bool, string) {
	if n, _ := s.client.Job.Query().Where(job.ProjectID(id), job.DeletedAtIsNil()).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete project — it has %d attached job(s)", n)
	}
	return true, ""
}

func (s *DependencyService) CanDeleteEstimate(ctx context.Context, id int64) (bool, string) {
	if n, _ := s.client.Invoice.Query().Where(invoice.EstimateID(id)).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete estimate — it has %d linked invoice(s)", n)
	}
	return true, ""
}

func (s *DependencyService) CanDeleteInvoice(ctx context.Context, id int64) (bool, string) {
	inv, err := s.client.Invoice.Get(ctx, id)
	if err != nil {
		return false, "Cannot verify invoice dependencies"
	}
	var payments []Payment
	if err := json.Unmarshal([]byte(inv.Payments), &payments); err == nil && len(payments) > 0 {
		return false, fmt.Sprintf("Cannot delete invoice — it has %d recorded payment(s)", len(payments))
	}
	return true, ""
}

func (s *DependencyService) CanDeleteAsset(ctx context.Context, id int64) (bool, string) {
	if n, _ := s.client.Job.Query().Where(job.AssetID(id), job.DeletedAtIsNil()).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete asset — it has %d linked job(s)", n)
	}
	return true, ""
}

func (s *DependencyService) CanDeleteItem(ctx context.Context, id int64) (bool, string) {
	var count int
	jobs, _ := s.client.Job.Query().Where(job.DeletedAtIsNil()).All(ctx)
	for _, j := range jobs {
		var items []LineItem
		if err := json.Unmarshal([]byte(j.LineItems), &items); err == nil {
			for _, li := range items {
				if li.ItemID == id {
					count++
					break
				}
			}
		}
	}
	estimates, _ := s.client.Estimate.Query().All(ctx)
	for _, e := range estimates {
		var items []LineItem
		if err := json.Unmarshal([]byte(e.LineItems), &items); err == nil {
			for _, li := range items {
				if li.ItemID == id {
					count++
					break
				}
			}
		}
	}
	invoices, _ := s.client.Invoice.Query().All(ctx)
	for _, i := range invoices {
		var items []LineItem
		if err := json.Unmarshal([]byte(i.LineItems), &items); err == nil {
			for _, li := range items {
				if li.ItemID == id {
					count++
					break
				}
			}
		}
	}
	if count > 0 {
		return false, fmt.Sprintf("Cannot delete item — it is used in %d document(s)", count)
	}
	return true, ""
}

func (s *DependencyService) CanDeleteAssetType(ctx context.Context, id int64) (bool, string) {
	if n, _ := s.client.Asset.Query().Where(asset.AssetTypeID(id), asset.DeletedAtIsNil()).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete type — %d asset(s) are assigned to it", n)
	}
	return true, ""
}

func (s *DependencyService) CanDeleteAssetStatus(ctx context.Context, id int64) (bool, string) {
	if n, _ := s.client.Asset.Query().Where(asset.AssetStatusID(id), asset.DeletedAtIsNil()).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete status — %d asset(s) are assigned to it", n)
	}
	return true, ""
}

func (s *DependencyService) CanDeleteTag(ctx context.Context, id int64) (bool, string) {
	if n, _ := s.client.TagLink.Query().Where(taglink.TagID(id)).Count(ctx); n > 0 {
		return false, fmt.Sprintf("Cannot delete tag — it is attached to %d item(s)", n)
	}
	return true, ""
}
