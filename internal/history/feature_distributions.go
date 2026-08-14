package history

import "atlas/pkg/api"

func (s *Service) FeatureDistributionSnapshots(limit int) ([]api.PredictionFeatureDistributionSnapshot, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []api.PredictionFeatureDistributionSnapshot
	err := s.db.Order("observed_at DESC, id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}
