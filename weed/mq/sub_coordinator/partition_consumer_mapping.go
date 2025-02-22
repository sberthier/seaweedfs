package sub_coordinator

import (
	"fmt"
	"github.com/seaweedfs/seaweedfs/weed/mq/topic"
	"time"
)

type PartitionConsumerMapping struct {
	currentMapping *PartitionSlotToConsumerInstanceList
	prevMappings   []*PartitionSlotToConsumerInstanceList
}

func NewPartitionConsumerMapping(ringSize int32) *PartitionConsumerMapping {
	newVersion := time.Now().UnixNano()
	return &PartitionConsumerMapping{
		currentMapping: NewPartitionSlotToConsumerInstanceList(ringSize, newVersion),
	}
}

// Balance goal:
// 1. max processing power utilization
// 2. allow one consumer instance to be down unexpectedly
//    without affecting the processing power utilization

func (pcm *PartitionConsumerMapping) BalanceToConsumerInstanceIds(partitions []*topic.Partition, consumerInstanceIds []string) {
	if len(partitions) == 0 || len(consumerInstanceIds) == 0 {
		return
	}
	newVersion := time.Now().UnixNano()
	newMapping := NewPartitionSlotToConsumerInstanceList(partitions[0].RingSize, newVersion)
	newMapping.PartitionSlots = doBalanceSticky(partitions, consumerInstanceIds, pcm.prevMappings[0])
	if pcm.currentMapping != nil {
		pcm.prevMappings = append(pcm.prevMappings, pcm.currentMapping)
	}
	pcm.currentMapping = newMapping
}

func doBalanceSticky(partitions []*topic.Partition, consumerInstanceIds []string, prevMapping *PartitionSlotToConsumerInstanceList) (partitionSlots []*PartitionSlotToConsumerInstance) {
	// collect previous consumer instance ids
	prevConsumerInstanceIds := make(map[string]struct{})
	if prevMapping != nil {
		for _, prevPartitionSlot := range prevMapping.PartitionSlots {
			if prevPartitionSlot.AssignedInstanceId != "" {
				prevConsumerInstanceIds[prevPartitionSlot.AssignedInstanceId] = struct{}{}
			}
		}
	}
	// collect current consumer instance ids
	currConsumerInstanceIds := make(map[string]struct{})
	for _, consumerInstanceId := range consumerInstanceIds {
		currConsumerInstanceIds[consumerInstanceId] = struct{}{}
	}

	// check deleted consumer instances
	deletedConsumerInstanceIds := make(map[string]struct{})
	for consumerInstanceId := range prevConsumerInstanceIds {
		if _, ok := currConsumerInstanceIds[consumerInstanceId]; !ok {
			deletedConsumerInstanceIds[consumerInstanceId] = struct{}{}
		}
	}

	// convert partition slots from list to a map
	prevPartitionSlotMap := make(map[string]*PartitionSlotToConsumerInstance)
	if prevMapping != nil {
		for _, partitionSlot := range prevMapping.PartitionSlots {
			key := fmt.Sprintf("%d-%d", partitionSlot.RangeStart, partitionSlot.RangeStop)
			prevPartitionSlotMap[key] = partitionSlot
		}
	}

	// make a copy of old mapping, skipping the deleted consumer instances
	newPartitionSlots := ToPartitionSlots(partitions)
	for _, newPartitionSlot := range newPartitionSlots {
		key := fmt.Sprintf("%d-%d", newPartitionSlot.RangeStart, newPartitionSlot.RangeStop)
		if prevPartitionSlot, ok := prevPartitionSlotMap[key]; ok {
			if _, ok := deletedConsumerInstanceIds[prevPartitionSlot.AssignedInstanceId]; !ok {
				newPartitionSlot.AssignedInstanceId = prevPartitionSlot.AssignedInstanceId
			}
		}
	}

	// for all consumer instances, count the average number of partitions
	// that are assigned to them
	consumerInstancePartitionCount := make(map[string]int)
	for _, newPartitionSlot := range newPartitionSlots {
		if newPartitionSlot.AssignedInstanceId != "" {
			consumerInstancePartitionCount[newPartitionSlot.AssignedInstanceId]++
		}
	}
	// average number of partitions that are assigned to each consumer instance
	averageConsumerInstanceLoad := float32(len(partitions)) / float32(len(consumerInstanceIds))

	// assign unassigned partition slots to consumer instances that is underloaded
	consumerInstanceIdsIndex := 0
	for _, newPartitionSlot := range newPartitionSlots {
		if newPartitionSlot.AssignedInstanceId == "" {
			for avoidDeadLoop := len(consumerInstanceIds); avoidDeadLoop > 0; avoidDeadLoop-- {
				consumerInstanceId := consumerInstanceIds[consumerInstanceIdsIndex]
				if float32(consumerInstancePartitionCount[consumerInstanceId]) < averageConsumerInstanceLoad {
					newPartitionSlot.AssignedInstanceId = consumerInstanceId
					consumerInstancePartitionCount[consumerInstanceId]++
					consumerInstanceIdsIndex++
					if consumerInstanceIdsIndex >= len(consumerInstanceIds) {
						consumerInstanceIdsIndex = 0
					}
					break
				} else {
					consumerInstanceIdsIndex++
					if consumerInstanceIdsIndex >= len(consumerInstanceIds) {
						consumerInstanceIdsIndex = 0
					}
				}
			}
		}
	}

	return newPartitionSlots
}
