package statusflow

import "testing"

func TestFixedCategorySemanticsIgnoreEditableLabels(t *testing.T) {
	if !IsClosed(JobCompleted) || !IsClosed(JobCanceled) || CountsAsCompletion(JobCanceled) {
		t.Fatal("job completion and closure semantics must remain category based")
	}
	if DefaultCreationCategory(Job) != JobNew || DefaultCreationCategory(Project) != ProjectNew ||
		DefaultCreationCategory(Estimate) != EstimateDraft || DefaultCreationCategory(Invoice) != InvoiceDraft {
		t.Fatal("creation defaults changed")
	}
	for _, category := range Categories {
		if (category.Key == InvoicePaid || category.Key == InvoicePartiallyPaid) && category.Manual {
			t.Fatalf("payment category %s is manual", category.Key)
		}
	}
}
