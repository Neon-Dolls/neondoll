package filetransfer

import "errors"

// ── Resume Coordination ─────────────────────────────────────────────────

// ResumeMatch checks whether a retained ReceiveState matches the given
// Offer, meaning the receiver can resume using the retained partial bytes.
// Returns the retained offset if match succeeds, or an error explaining
// why resume is not possible.
func ResumeMatch(retained *ReceiveState, offer *Offer) (int64, error) {
	if retained == nil {
		return 0, errors.New("filetransfer: resume: no retained state")
	}
	if offer == nil {
		return 0, errors.New("filetransfer: resume: nil offer")
	}

	if retained.FileID != offer.FileID {
		return 0, errors.New("filetransfer: resume: file_id mismatch")
	}
	if retained.Size != offer.Size {
		return 0, errors.New("filetransfer: resume: size mismatch")
	}
	if retained.SHA256 != offer.SHA256 {
		return 0, errors.New("filetransfer: resume: SHA-256 mismatch")
	}
	if retained.IsCompleted() {
		return 0, errors.New("filetransfer: resume: file already completed")
	}

	return retained.Retained, nil
}
