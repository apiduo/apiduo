// Package modelalias resolves a channel's model mapping without a request
// context.
//
// Both the relay and the asynchronous worker need the same answer: a mapping is
// how an operator declares that a customer-facing name means a particular vendor
// name. The worker resolves it per channel at execution time, because a task can
// move between channels after submission.
package modelalias

import (
	"errors"
	"fmt"

	rootcommon "github.com/QuantumNous/new-api/common"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
)

// ApplyModelMapping resolves a channel's mapping chain for one model name.
//
// The chain is followed to its end, because operators use it to point a
// customer-facing name at a vendor's name, and sometimes at a second alias.
// A cycle is an error rather than a silent stop: a mapping that loops would
// otherwise resolve to whichever entry happened to come first.
func ApplyModelMapping(mappingJSON, originModel string) (upstream string, mapped bool, err error) {
	// No mapping is the common case, and it is not an error: an empty string is
	// not JSON, and treating it as a parse failure would refuse every channel
	// that does not map anything.
	if mappingJSON == "" || mappingJSON == "{}" {
		return originModel, false, nil
	}
	modelMap := make(map[string]string)
	if err := rootcommon.Unmarshal([]byte(mappingJSON), &modelMap); err != nil {
		return "", false, fmt.Errorf("unmarshal_model_mapping_failed")
	}

	currentModel := originModel
	visitedModels := map[string]bool{currentModel: true}
	for {
		mappedModel, exists := modelMap[currentModel]
		baseModel := hostreasoning.BaseModelName(currentModel)
		if (!exists || mappedModel == "") && baseModel != currentModel {
			mappedModel, exists = modelMap[baseModel]
		}
		if !exists || mappedModel == "" {
			break
		}
		if visitedModels[mappedModel] {
			if mappedModel == currentModel {
				if currentModel == originModel {
					return originModel, false, nil
				}
				return currentModel, true, nil
			}
			return "", false, errors.New("model_mapping_contains_cycle")
		}
		visitedModels[mappedModel] = true
		currentModel = mappedModel
		mapped = true
	}
	if !mapped {
		return originModel, false, nil
	}
	return currentModel, true, nil
}
