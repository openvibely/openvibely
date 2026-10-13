package persistence

type XReceiptClaimResult string

const (
	XReceiptClaimed   XReceiptClaimResult = "claimed"
	XReceiptActive    XReceiptClaimResult = "active"
	XReceiptCompleted XReceiptClaimResult = "completed"
)

type XReceiptClaim struct {
	Result XReceiptClaimResult
	Token  string
}
