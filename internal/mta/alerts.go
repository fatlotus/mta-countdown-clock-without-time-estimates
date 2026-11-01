package mta

import (
	"fmt"
	"strings"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
)

// extractTranslatedText extracts plain English or first non-empty translation text from a TranslatedString.
func extractTranslatedText(ts *gtfs.TranslatedString) string {
	if ts == nil {
		return ""
	}
	var fallback string
	for _, tr := range ts.GetTranslation() {
		text := strings.TrimSpace(tr.GetText())
		if text == "" {
			continue
		}
		lang := strings.ToLower(tr.GetLanguage())
		if lang == "en" || lang == "en-us" {
			return text
		}
		if fallback == "" {
			fallback = text
		}
	}
	return fallback
}

// parseFeedToAlerts parses a GTFS-RT Alerts feed into structured ServiceAlert objects.
func parseFeedToAlerts(feed *gtfs.FeedMessage, nowSec int64) ([]ServiceAlert, int64) {
	if feed == nil {
		return []ServiceAlert{}, nowSec
	}

	feedTS := int64(0)
	if feed.GetHeader() != nil {
		feedTS = int64(feed.GetHeader().GetTimestamp())
	}
	if feedTS == 0 {
		feedTS = nowSec
	}

	alerts := make([]ServiceAlert, 0, len(feed.GetEntity()))

	for idx, entity := range feed.GetEntity() {
		if entity == nil {
			continue
		}
		alert := entity.GetAlert()
		if alert == nil {
			continue
		}

		headerText := extractTranslatedText(alert.GetHeaderText())
		if headerText == "" {
			continue
		}
		descriptionText := extractTranslatedText(alert.GetDescriptionText())

		// Active periods filtering:
		// If len(alert.GetActivePeriod()) > 0, keep periods where (end == 0 || end >= nowSec) and start <= nowSec + 14*86400.
		// If all periods in a non-empty GetActivePeriod() were filtered out, skip the alert.
		var activePeriods []AlertTimeWindow
		rawPeriods := alert.GetActivePeriod()
		if len(rawPeriods) > 0 {
			for _, ap := range rawPeriods {
				if ap == nil {
					continue
				}
				start := int64(ap.GetStart())
				end := int64(ap.GetEnd())
				if (end == 0 || end >= nowSec) && start <= nowSec+14*86400 {
					activePeriods = append(activePeriods, AlertTimeWindow{
						Start: start,
						End:   end,
					})
				}
			}
			if len(activePeriods) == 0 {
				continue
			}
		}

		// Informed entities: routes and directional stop IDs
		var routes []string
		seenRoutes := make(map[string]bool)
		var stopIDs []string
		seenStopIDs := make(map[string]bool)
		hasDir0 := false
		hasDir1 := false

		for _, ie := range alert.GetInformedEntity() {
			if ie == nil {
				continue
			}
			if r := strings.TrimSpace(ie.GetRouteId()); r != "" {
				if !seenRoutes[r] {
					seenRoutes[r] = true
					routes = append(routes, r)
				}
			}
			if rawStop := strings.TrimSpace(ie.GetStopId()); rawStop != "" {
				baseID := CleanStopID(rawStop)
				var sID string
				if strings.HasSuffix(rawStop, "N") || (ie.DirectionId != nil && ie.GetDirectionId() == 0) {
					sID = baseID + "N"
				} else if strings.HasSuffix(rawStop, "S") || (ie.DirectionId != nil && ie.GetDirectionId() == 1) {
					sID = baseID + "S"
				} else {
					sID = baseID
				}
				if !seenStopIDs[sID] {
					seenStopIDs[sID] = true
					stopIDs = append(stopIDs, sID)
				}
			}
			if ie.DirectionId != nil {
				if ie.GetDirectionId() == 0 {
					hasDir0 = true
				} else if ie.GetDirectionId() == 1 {
					hasDir1 = true
				}
			}
		}

		direction := ""
		if len(stopIDs) == 0 {
			if hasDir0 && !hasDir1 {
				direction = "N"
			} else if hasDir1 && !hasDir0 {
				direction = "S"
			}
		}

		entityID := strings.TrimSpace(entity.GetId())
		if entityID == "" {
			entityID = fmt.Sprintf("alert_%d", idx)
		}

		alertType := "SERVICE ALERT"
		if strings.Contains(strings.ToLower(entityID), "planned_work") {
			alertType = "PLANNED WORK"
		}

		alerts = append(alerts, ServiceAlert{
			ID:            entityID,
			AlertType:     alertType,
			Routes:        routes,
			StopIDs:       stopIDs,
			Direction:     direction,
			HeaderText:    headerText,
			Description:   descriptionText,
			ActivePeriods: activePeriods,
		})
	}

	return alerts, feedTS
}

// projectStationAlerts projects system alerts to a target station and its platforms.
func (s *Service) projectStationAlerts(st Station, allowedRoutesSet map[string]bool, alerts []ServiceAlert, nowSec int64) (stationAlerts, northAlerts, southAlerts []ServiceAlert) {
	stationAlerts = []ServiceAlert{}
	northAlerts = []ServiceAlert{}
	southAlerts = []ServiceAlert{}

	for _, al := range alerts {
		// 1. Time window check:
		// An alert is within window if len(al.ActivePeriods) == 0 or any period has
		// (nowSec >= ap.Start && (ap.End == 0 || nowSec <= ap.End)) || (ap.Start > nowSec && ap.Start <= nowSec+86400)
		if len(al.ActivePeriods) > 0 {
			active := false
			for _, ap := range al.ActivePeriods {
				if (nowSec >= ap.Start && (ap.End == 0 || nowSec <= ap.End)) || (ap.Start > nowSec && ap.Start <= nowSec+86400) {
					active = true
					break
				}
			}
			if !active {
				continue
			}
		}

		// 2. Route match check:
		// Route matches if len(al.Routes) == 0 or any r in al.Routes is in allowedRoutesSet (or s.normalizeRouteID(r) is in allowedRoutesSet).
		if len(allowedRoutesSet) > 0 && len(al.Routes) > 0 {
			routeMatched := false
			for _, r := range al.Routes {
				if allowedRoutesSet[r] || allowedRoutesSet[s.normalizeRouteID(r)] {
					routeMatched = true
					break
				}
			}
			if !routeMatched {
				continue
			}
		}

		// 3. Station & direction matching
		if len(al.StopIDs) == 0 {
			// Line-wide alert
			matchesN := al.Direction == "" || al.Direction == "N"
			matchesS := al.Direction == "" || al.Direction == "S"
			if !matchesN && !matchesS {
				continue
			}
			stationAlerts = append(stationAlerts, al)
			if matchesN {
				northAlerts = append(northAlerts, al)
			}
			if matchesS {
				southAlerts = append(southAlerts, al)
			}
		} else {
			// Station-specific alert
			hasBoth := containsStr(al.StopIDs, st.ID)
			hasN := hasBoth || containsStr(al.StopIDs, st.ID+"N")
			hasS := hasBoth || containsStr(al.StopIDs, st.ID+"S")
			if !hasN && !hasS {
				continue
			}

			stAl := al
			if hasN && !hasS {
				stAl.Direction = "N"
			} else if hasS && !hasN {
				stAl.Direction = "S"
			} else {
				stAl.Direction = ""
			}
			stationAlerts = append(stationAlerts, stAl)

			if hasN {
				northAlerts = append(northAlerts, al)
			}
			if hasS {
				southAlerts = append(southAlerts, al)
			}
		}
	}

	return stationAlerts, northAlerts, southAlerts
}
