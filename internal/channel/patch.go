package channel

// Patch is a validated partial update; nil means "not provided".
type Patch struct {
	Name           *string
	URL            *string
	Logo           *string
	Group          *string
	Number         *float64 // set together with NumberSet; NumberSet && Number==nil clears it
	NumberSet      bool
	Enabled        *bool
	Order          *float64
	Sources        *[]RawSource
	ActiveSourceID *string
}

// Merge applies p to the current channel so url and sources always stay consistent:
//   - url only (legacy admin/clients) → the active source's url changes too
//   - sources only → url is recomputed from the active source
func Merge(current Channel, p Patch) Raw {
	r := current.Raw()
	if p.Name != nil {
		r.Name = *p.Name
	}
	if p.Logo != nil {
		r.Logo = *p.Logo
	}
	if p.Group != nil {
		r.Group = *p.Group
	}
	if p.NumberSet {
		r.Number = p.Number
	}
	if p.Enabled != nil {
		r.Enabled = p.Enabled
	}
	if p.Order != nil {
		r.Order = p.Order
	}
	if p.ActiveSourceID != nil {
		r.ActiveSourceID = *p.ActiveSourceID
	}
	switch {
	case p.Sources != nil && p.URL == nil:
		r.Sources = *p.Sources
		r.URL = ""
	case p.URL != nil && p.Sources == nil:
		r.URL = *p.URL
		for i := range r.Sources {
			if r.Sources[i].ID == current.ActiveID() {
				r.Sources[i].URL = *p.URL
			}
		}
	default:
		if p.Sources != nil {
			r.Sources = *p.Sources
		}
		if p.URL != nil {
			r.URL = *p.URL
		}
	}
	return r
}
