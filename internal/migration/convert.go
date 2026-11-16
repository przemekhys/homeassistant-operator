package migration

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertReport accepts only classifications with a verified mapper. Other
// kinds remain fail-closed rather than being represented by approximate CRs.
func ConvertReport(report *Report) error {
	if report == nil {
		return fmt.Errorf("migration report is required")
	}
	seen := map[string]bool{}
	for _, object := range report.Objects {
		if object.Classification == ClassificationImportable {
			if object.Kind != "Floor" {
				return fmt.Errorf("no verified conversion exists for %s", object.Kind)
			}
			if _, err := decodeFloorProposal(object); err != nil {
				return err
			}
			if seen[object.Kind+"/"+object.SourceID] {
				return fmt.Errorf("duplicate importable identity")
			}
			seen[object.Kind+"/"+object.SourceID] = true
		}
	}
	return nil
}

func decodeFloorProposal(object ReportObject) (floorProposal, error) {
	var proposal floorProposal
	fields, err := uniqueObject(object.Proposal)
	if err != nil || len(fields) != 3 || object.SourceID == "" ||
		strings.TrimSpace(
			object.SourceID,
		) != object.SourceID || object.Name != floorName(object.SourceID) {
		return proposal, fmt.Errorf("invalid floor proposal")
	}
	for _, key := range []string{"name", "icon", "level"} {
		if _, exists := fields[key]; !exists {
			return proposal, fmt.Errorf("incomplete floor proposal")
		}
	}
	if string(fields["name"]) == "null" || string(fields["icon"]) == "null" ||
		json.Unmarshal(
			object.Proposal,
			&proposal,
		) != nil || strings.TrimSpace(proposal.Name) == "" {
		return floorProposal{}, fmt.Errorf("invalid floor proposal")
	}
	return proposal, nil
}
