package store

const DefaultPageSize = 10
const MaxPageSize = 100

type Pagination struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
	Total    int `json:"total"`
}

// Clamp before calculating offsets, including after deletions shrink the list.
func (p Pagination) WithTotal(total int) Pagination {
	p.Total = total
	if p.PageSize < 1 {
		p.PageSize = DefaultPageSize
	}
	if p.PageSize > MaxPageSize {
		p.PageSize = MaxPageSize
	}
	last := max(1, (total+p.PageSize-1)/p.PageSize)
	p.Page = min(max(1, p.Page), last)
	return p
}

func (p Pagination) offset() int { return (p.Page - 1) * p.PageSize }
