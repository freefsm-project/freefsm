package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
)

func Seed(ctx context.Context, client *ent.Client) error {
	// Idempotency check
	count, err := client.Customer.Query().Count(ctx)
	if err != nil {
		return fmt.Errorf("check existing data: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("database already has %d customer(s); seeding skipped", count)
	}

	now := time.Now()

	// --- Customers (5) ---
	customers := make([]*ent.Customer, 5)
	customerData := []struct {
		DisplayName string
		Email       string
		Phone       string
		CompanyName string
		Addr1       string
		City        string
		State       string
		Zip         string
	}{
		{"Acme Office Park", "facilities@acme.com", "555-0101", "Acme Corp", "123 Main St", "Dallas", "TX", "75201"},
		{"Metro Mall", "ops@metromall.com", "555-0102", "Metro Properties", "456 Commerce Blvd", "Houston", "TX", "77001"},
		{"Downtown Tower", "maint@downtown.tower", "555-0103", "Tower Management", "789 Skyline Ave", "Austin", "TX", "78701"},
		{"Regional Hospital", "engineering@regionalhosp.org", "555-0104", "Regional Health", "321 Health Way", "San Antonio", "TX", "78201"},
		{"Tech Campus", "facilities@techcampus.com", "555-0105", "Tech Campus Inc", "654 Innovation Dr", "Plano", "TX", "75024"},
	}
	for i, d := range customerData {
		c, err := client.Customer.Create().
			SetDisplayName(d.DisplayName).
			SetEmail(d.Email).
			SetPhone(d.Phone).
			SetCompanyName(d.CompanyName).
			SetBillingAddress1(d.Addr1).
			SetBillingCity(d.City).
			SetBillingState(d.State).
			SetBillingZipCode(d.Zip).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create customer %d: %w", i, err)
		}
		customers[i] = c
	}

	// --- Assets (8) ---
	assets := make([]*ent.Asset, 8)
	assetData := []struct {
		Name          string
		SerialNumber  string
		Model         string
		Manufacturer  string
		AssetTypeID   int64
		AssetStatusID int64
		CustIdx       int
		Notes         string
		InstalledYr   int
		WarrantyYr    int
	}{
		{"Rooftop Unit RTU-1", "SN-ACME-001", "Trane Voyager 12.5T", "Trane", 1, 1, 0, "Primary rooftop unit for Building A", 2019, 2029},
		{"Rooftop Unit RTU-2", "SN-ACME-002", "Trane Voyager 12.5T", "Trane", 1, 1, 0, "Secondary rooftop unit for Building B", 2019, 2029},
		{"Backup Generator G1", "SN-METRO-001", "Cummins C150D6", "Cummins", 2, 1, 1, "Emergency backup generator for mall common areas", 2021, 2031},
		{"Chiller Unit CH-01", "SN-TOWER-001", "Carrier 30XA 200T", "Carrier", 1, 3, 2, "Primary chiller showing bearing wear — scheduled rebuild", 2018, 2028},
		{"Walk-in Cooler W1", "SN-HOSP-001", "Norlake KLB7788-C", "Norlake", 4, 1, 3, "Pharmacy walk-in cooler, temp verified weekly", 2020, 2030},
		{"Hot Water Heater H1", "SN-HOSP-002", "A.O. Smith BTX-100", "A.O. Smith", 3, 1, 3, "Domestic hot water for east wing", 2022, 2032},
		{"Server Room CRAC", "SN-TECH-001", "Liebert CRV 040", "Liebert", 1, 1, 4, "In-row cooling for server rack cluster", 2023, 2033},
		{"Portable Heater P1", "SN-TECH-002", "Modine HSB 47", "Modine", 5, 4, 4, "Retired portable unit, kept for parts", 2015, 2020},
	}
	for i, d := range assetData {
		installed := time.Date(d.InstalledYr, 1, 15, 0, 0, 0, 0, time.UTC)
		warranty := time.Date(d.WarrantyYr, 1, 15, 0, 0, 0, 0, time.UTC)
		a, err := client.Asset.Create().
			SetName(d.Name).
			SetSerialNumber(d.SerialNumber).
			SetModel(d.Model).
			SetManufacturer(d.Manufacturer).
			SetAssetTypeID(d.AssetTypeID).
			SetAssetStatusID(d.AssetStatusID).
			SetCustomerID(customers[d.CustIdx].ID).
			SetNotes(d.Notes).
			SetInstalledAt(installed).
			SetWarrantyExpires(warranty).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create asset %d: %w", i, err)
		}
		assets[i] = a
	}

	// --- Items (5) ---
	items := make([]*ent.Item, 5)
	itemData := []struct {
		Name      string
		Type      string
		UnitPrice float64
		Desc      string
	}{
		{"Labor - HVAC Technician", "service", 125.00, "Standard hourly rate for certified HVAC technician"},
		{"Compressor Unit - 5 Ton", "product", 1850.00, "Commercial grade compressor unit"},
		{"Refrigerant Recharge - R410A", "service", 350.00, "Per pound refrigerant recharge with leak inspection"},
		{"Duct Cleaning - Commercial", "service", 850.00, "Complete commercial duct cleaning service"},
		{"Thermostat - Smart WiFi", "product", 245.00, "Programmable smart thermostat with remote monitoring"},
	}
	for i, d := range itemData {
		it, err := client.Item.Create().
			SetName(d.Name).
			SetType(d.Type).
			SetUnitPrice(d.UnitPrice).
			SetDescription(d.Desc).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create item %d: %w", i, err)
		}
		items[i] = it
	}

	// --- Tags (5) ---
	tags := make([]*ent.Tag, 5)
	tagData := []struct {
		Name  string
		Color string
	}{
		{"Commercial", "#3B82F6"},
		{"Emergency", "#EF4444"},
		{"Maintenance", "#10B981"},
		{"Installation", "#8B5CF6"},
		{"Warranty", "#F59E0B"},
	}
	for i, d := range tagData {
		t, err := client.Tag.Create().
			SetName(d.Name).
			SetColor(d.Color).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create tag %d: %w", i, err)
		}
		tags[i] = t
	}

	// --- Projects (5) ---
	projects := make([]*ent.Project, 5)
	projectData := []struct {
		Name        string
		Description string
		CustIdx     int
	}{
		{"Building A HVAC Retrofit", "Complete HVAC system replacement for 12-story office building", 1},
		{"Server Room Cooling Upgrade", "Install dedicated cooling system for data center", 4},
		{"Warehouse Climate Control", "Temperature and humidity control for 50k sq ft warehouse", 2},
		{"Hospital Wing Renovation", "HVAC design and install for new patient wing", 3},
		{"Campus Chiller Replacement", "Replace aging chiller units across 4 buildings", 4},
	}
	for i, d := range projectData {
		p, err := client.Project.Create().
			SetName(d.Name).
			SetDescription(d.Description).
			SetCustomerID(customers[d.CustIdx].ID).
			SetCompletionPercentage(float64(i+1) * 15).
			SetStartTime(now.AddDate(1, -(i+1)*2, 0)).
			SetEndTime(now.AddDate(1, i+1, 0)).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create project %d: %w", i, err)
		}
		projects[i] = p
	}

	// --- Jobs (5) ---
	jobs := make([]*ent.Job, 5)
	jobData := []struct {
		JobType   string
		Subtitle  string
		CustIdx   int
		ProjIdx   int
		AssetIdx  int
		StatusID  int64
		StartOff  time.Duration
		EndOff    time.Duration
		Notes     string
		TechNotes string
	}{
		{"AC Repair", "Building 2 Unit B3", 0, 0, 0, 1, -24 * time.Hour, -2 * time.Hour, "Unit not cooling, low refrigerant suspected", "Found leak at evaporator coil, recharged system"},
		{"Chiller Maintenance", "Primary chiller Q2 service", 4, 4, 3, 4, -72 * time.Hour, -48 * time.Hour, "Scheduled quarterly maintenance", "Replaced worn bearings, checked compressor amp draw"},
		{"Thermostat Install", "Admin wing smart thermostats", 1, 0, -1, 2, -168 * time.Hour, -144 * time.Hour, "Install 24 smart thermostats with centralized control", "All units online, programming complete"},
		{"Duct Cleaning", "Main distribution ducts", 2, 2, -1, 3, -96 * time.Hour, -80 * time.Hour, "Annual duct cleaning per maintenance contract", "Removed 18 lbs of debris, sanitized all runs"},
		{"Compressor Replacement", "Unit C7 rooftop", 3, 3, -1, 4, -48 * time.Hour, -24 * time.Hour, "Compressor seized, emergency replacement", "New unit online, pressures nominal, customer approved"},
	}
	for i, d := range jobData {
		b := client.Job.Create().
			SetJobType(d.JobType).
			SetSubtitle(d.Subtitle).
			SetCustomerID(customers[d.CustIdx].ID).
			SetProjectID(projects[d.ProjIdx].ID).
			SetStatusID(d.StatusID).
			SetStartTime(now.Add(d.StartOff)).
			SetEndTime(now.Add(d.EndOff)).
			SetNotes(d.Notes).
			SetTechNotes(d.TechNotes).
			SetBillingType("flat_rate")
		if d.AssetIdx >= 0 {
			b.SetAssetID(assets[d.AssetIdx].ID)
		}
		j, err := b.Save(ctx)
		if err != nil {
			return fmt.Errorf("create job %d: %w", i, err)
		}
		jobs[i] = j
	}

	// --- Tag Links for Jobs ---
	for i, j := range jobs {
		_, err := client.TagLink.Create().
			SetTagID(tags[i%5].ID).
			SetObjectType("job").
			SetObjectID(j.ID).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create tag link %d: %w", i, err)
		}
	}

	// --- Estimates (5) ---
	estimates := make([]*ent.Estimate, 5)
	estData := []struct {
		Title    string
		CustIdx  int
		JobIdx   int
		StatusID int64
		TaxRate  string
		Items    []map[string]interface{}
	}{
		{"HVAC Repair Estimate - Acme", 0, 0, 2, "0.0825", nil},
		{"Chiller Service Quote - Regional", 3, 1, 2, " 0.0825", nil},
		{"Smart Thermostat Proposal - Metro", 1, 2, 3, " 0.0825", nil},
		{"Duct Cleaning Estimate - Mall", 2, 3, 2, "0.0825", nil},
		{"Compressor Replacement Quote - Tower", 3, 4, 1, "0.0825", nil},
	}

	for i, d := range estData {
		lineItems := []map[string]interface{}{
			{"item_id": items[0].ID, "title": items[1].Name, "description": items[1].Description, "unit_price": items[1].UnitPrice, "quantity": 1, "taxable": true, "tax_rate": "0.0825", "discount": 0, "surcharge": 0},
			{"item_id": items[0].ID, "title": items[0].Name, "description": items[0].Description, "unit_price": items[0].UnitPrice, "quantity": 4, "taxable": true, "tax_rate": "0.0825", "discount": 0, "surcharge": 150},
		}
		liJSON, _ := json.Marshal(lineItems)

		e, err := client.Estimate.Create().
			SetTitle(d.Title).
			SetCustomerID(customers[d.CustIdx].ID).
			SetJobID(jobs[d.JobIdx].ID).
			SetStatusID(d.StatusID).
			SetTaxRate(d.TaxRate).
			SetLineItems(string(liJSON)).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create estimate %d: %w", i, err)
		}
		estimates[i] = e
	}

	// --- Invoices (5) ---
	invoiceData := []struct {
		Title       string
		CustIdx     int
		JobIdx      int
		EstimateIdx int
		StatusID    int64
		TaxRate     string
		Paid        bool
	}{
		{"Invoice - Acme Office Repair", 0, 1, -1, 3, "0.0825", true},
		{"Invoice - Regional Hospital Maint", 3, 1, 1, 2, "0.0825", false},
		{"Invoice - Metro Mall Thermostats", 1, 2, 2, 2, "0.0825", false},
		{"Invoice - Downtown Tower Compressor", 3, 4, 4, 2, "0.0825", false},
		{"Invoice - Warehouse Duct Cleaning", 2, 3, 3, 3, "0.0825", true},
	}

	for i, d := range invoiceData {
		lineItems := []map[string]interface{}{
			{"item_id": items[0].ID, "title": items[1].Name, "description": items[1].Description, "unit_price": items[1].UnitPrice, "quantity": 1, "taxable": true, "tax_rate": "0.0825", "discount": 0, "surcharge": 0},
			{"item_id": items[0].ID, "title": items[0].Name, "description": items[0].Description, "unit_price": items[0].UnitPrice, "quantity": 3, "taxable": true, "tax_rate": "0.0825", "discount": 0, "surcharge": 0},
		}
		liJSON, _ := json.Marshal(lineItems)

		invCreate := client.Invoice.Create().
			SetInvoiceNumber(int64(i + 1)).
			SetTitle(d.Title).
			SetCustomerID(customers[d.CustIdx].ID).
			SetJobID(jobs[d.JobIdx].ID).
			SetStatusID(d.StatusID).
			SetTaxRate(d.TaxRate).
			SetLineItems(string(liJSON)).
			SetInvoiceDate(now.AddDate(1, -(i + 1), 0)).
			SetDueDate(now.AddDate(1, -(i+1)+1, 0))

		if d.EstimateIdx >= 0 {
			invCreate.SetEstimateID(estimates[d.EstimateIdx].ID)
		}

		inv, err := invCreate.Save(ctx)
		if err != nil {
			return fmt.Errorf("create invoice %d: %w", i, err)
		}

		// Add payment for paid invoices
		if d.Paid {
			payments := []map[string]interface{}{
				{"amount": 2850.00 + float64(i)*150, "method": "check", "date": now.Format("2006-01-02"), "reference": fmt.Sprintf("CHK-%04d", 1000+i)},
			}
			payJSON, _ := json.Marshal(payments)
			_, err = inv.Update().SetPayments(string(payJSON)).Save(ctx)
			if err != nil {
				return fmt.Errorf("add payment to invoice %d: %w", i, err)
			}
		}
	}

	// --- Time Entries (5) for admin user (ID 1) ---
	for i := 0; i < 5; i++ {
		clockIn := now.AddDate(0, 0, -(i + 1)).Add(-8 * time.Hour)
		clockOut := clockIn.Add(7*time.Hour + 30*time.Minute)
		_, err := client.TimeEntry.Create().
			SetUserID(1).
			SetClockIn(clockIn).
			SetClockOut(clockOut).
			SetNotes(fmt.Sprintf("Routine maintenance day %d", i+1)).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create time entry %d: %w", i, err)
		}
	}

	return nil
}
