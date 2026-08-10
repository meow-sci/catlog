package stats_test

import (
	"database/sql"
	"testing"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/testutil"
)

func TestFlightFactsMilestonesCareerAndOrdering(t *testing.T) {
	zeroFlight := flightN(31)
	absentFlight := flightN(32)
	positiveFlight := flightN(33)
	careerFlight := flightN(34)
	engines := 3
	proj := testutil.MemProjections(t)

	err := proj.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		batch := stats.NewBatch(tx, stats.BatchOptions{})
		applyFlight := func(seq int64, in input) error {
			return stats.FlightFold().Apply(t.Context(), batch, decode(t, in, seq))
		}

		// Orbit is a raw, set-only milestone even before flight.started. An SOI
		// before the launch body is known is conservatively declined forever.
		if err := applyFlight(1, input{flight: zeroFlight, career: "firstcareer00001", typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "achieved"}}); err != nil {
			return err
		}
		if err := applyFlight(2, input{flight: zeroFlight, career: "latercareer00001", typ: "vehicle.orbit", payload: stats.VehicleOrbit{Phase: "escaped"}}); err != nil {
			return err
		}
		if err := applyFlight(3, input{flight: zeroFlight, typ: "vehicle.atmosphere", payload: stats.VehicleAtmosphere{Dir: "entered"}}); err != nil {
			return err
		}
		if err := applyFlight(4, input{flight: zeroFlight, typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}}); err != nil {
			return err
		}
		beforeStart, ok, err := batch.Flight(t.Context(), zeroFlight)
		if err != nil || !ok {
			t.Fatalf("flight before start = ok %v, err %v", ok, err)
		}
		if beforeStart.StartedSeq != 0 || beforeStart.Milestones != stats.MilestoneOrbit || beforeStart.FirstOrbitSeq != 1 {
			t.Errorf("before start = start %d milestones %d orbit %d, want 0, orbit, 1", beforeStart.StartedSeq, beforeStart.Milestones, beforeStart.FirstOrbitSeq)
		}
		if beforeStart.HasStartFactAt(4, true) {
			t.Error("placeholder flight row was treated as an actual flight.started fact")
		}

		if err := applyFlight(5, input{flight: zeroFlight, career: "latercareer00001", typ: "flight.started", payload: stats.FlightStarted{
			Body: "earth", PartCount: 0, MassKg: 0,
		}}); err != nil {
			return err
		}
		for seq, in := range []input{
			{flight: zeroFlight, typ: "vehicle.atmosphere", payload: stats.VehicleAtmosphere{Dir: "exited"}},
			{flight: zeroFlight, typ: "vehicle.landed", payload: stats.VehicleLanded{Survived: false}},
			{flight: zeroFlight, typ: "vehicle.landed", payload: stats.VehicleLanded{Survived: true}},
			{flight: zeroFlight, typ: "vehicle.undocked", payload: stats.VehicleDock{}},
			{flight: zeroFlight, typ: "vehicle.docked", payload: stats.VehicleDock{}},
			{flight: zeroFlight, typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: ""}},
			{flight: zeroFlight, typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "earth"}},
			{flight: zeroFlight, typ: "vehicle.soi", payload: stats.VehicleSOI{ToBody: "luna"}},
		} {
			if err := applyFlight(int64(seq+6), in); err != nil {
				return err
			}
		}

		// A non-start flight has absent launch facts. A positive start preserves
		// all positive values. Both are asserted again after flush/reload below.
		if err := applyFlight(20, input{flight: absentFlight, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "console"}}); err != nil {
			return err
		}
		if err := applyFlight(21, input{flight: positiveFlight, typ: "flight.started", payload: stats.FlightStarted{
			Body: "duna", PartCount: 17, MassKg: 1234.5, EngineCount: &engines,
		}}); err != nil {
			return err
		}

		// Empty career does not claim the flight. The first later nonempty value
		// does, and a different later value cannot overwrite it.
		if err := applyFlight(22, input{flight: careerFlight, noCareer: true, typ: "flight.flagged", payload: stats.FlightFlagged{Flag: "refuel"}}); err != nil {
			return err
		}
		if err := applyFlight(23, input{flight: careerFlight, career: "careerwinner001", typ: "vehicle.atmosphere", payload: stats.VehicleAtmosphere{Dir: "entered"}}); err != nil {
			return err
		}
		if err := applyFlight(24, input{flight: careerFlight, career: "careermustlose01", typ: "vehicle.docked", payload: stats.VehicleDock{}}); err != nil {
			return err
		}

		assertFlightFacts(t, batch, zeroFlight, stats.FlightState{
			StartedSeq: 5, Milestones: stats.MilestoneOrbit | stats.MilestoneSpace | stats.MilestoneOtherSOI | stats.MilestoneLanded | stats.MilestoneDocked, FirstOrbitSeq: 1,
			PartCount: sql.NullInt64{Int64: 0, Valid: true}, LaunchMassKg: sql.NullFloat64{Float64: 0, Valid: true}, Career: "firstcareer00001",
		})
		assertFlightFacts(t, batch, absentFlight, stats.FlightState{Career: defaultCareer})
		assertFlightFacts(t, batch, positiveFlight, stats.FlightState{
			StartedSeq: 21, EngineCount: sql.NullInt64{Int64: 3, Valid: true},
			PartCount: sql.NullInt64{Int64: 17, Valid: true}, LaunchMassKg: sql.NullFloat64{Float64: 1234.5, Valid: true}, Career: defaultCareer,
		})
		assertFlightFacts(t, batch, careerFlight, stats.FlightState{Career: "careerwinner001", Milestones: stats.MilestoneDocked})

		zero, _, _ := batch.Flight(t.Context(), zeroFlight)
		if zero.HasStartFactAt(4, zero.PartCount.Valid) {
			t.Error("late flight.started made its part_count usable by an earlier candidate")
		}
		if !zero.HasStartFactAt(5, zero.PartCount.Valid) {
			t.Error("present zero part_count was not usable at flight.started")
		}
		if zero.HasStartFactAt(6, zero.EngineCount.Valid) {
			t.Error("absent engine_count was treated as a usable start fact")
		}

		return batch.Flush(t.Context())
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := proj.WithWriteTx(t.Context(), func(tx *sql.Tx) error {
		batch := stats.NewBatch(tx, stats.BatchOptions{})
		if err := stats.FlightFold().Apply(t.Context(), batch, decode(t, input{
			flight: careerFlight, career: "afterreloadloses1", typ: "vehicle.atmosphere",
			payload: stats.VehicleAtmosphere{Dir: "entered"},
		}, 25)); err != nil {
			return err
		}
		assertFlightFacts(t, batch, zeroFlight, stats.FlightState{
			StartedSeq: 5, Milestones: stats.MilestoneOrbit | stats.MilestoneSpace | stats.MilestoneOtherSOI | stats.MilestoneLanded | stats.MilestoneDocked, FirstOrbitSeq: 1,
			PartCount: sql.NullInt64{Int64: 0, Valid: true}, LaunchMassKg: sql.NullFloat64{Float64: 0, Valid: true}, Career: "firstcareer00001",
		})
		assertFlightFacts(t, batch, absentFlight, stats.FlightState{Career: defaultCareer})
		assertFlightFacts(t, batch, positiveFlight, stats.FlightState{
			StartedSeq: 21, EngineCount: sql.NullInt64{Int64: 3, Valid: true},
			PartCount: sql.NullInt64{Int64: 17, Valid: true}, LaunchMassKg: sql.NullFloat64{Float64: 1234.5, Valid: true}, Career: defaultCareer,
		})
		assertFlightFacts(t, batch, careerFlight, stats.FlightState{Career: "careerwinner001", Milestones: stats.MilestoneDocked})
		return batch.Flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func assertFlightFacts(t *testing.T, batch *stats.Batch, flight ids.ID, want stats.FlightState) {
	t.Helper()
	got, ok, err := batch.Flight(t.Context(), flight)
	if err != nil || !ok {
		t.Fatalf("Flight(%s) = ok %v, err %v", ids.String(flight), ok, err)
	}
	if got.StartedSeq != want.StartedSeq || got.EngineCount != want.EngineCount ||
		got.Milestones != want.Milestones || got.PartCount != want.PartCount ||
		got.LaunchMassKg != want.LaunchMassKg || got.Career != want.Career {
		t.Errorf("Flight(%s) facts = {seq:%d engine:%+v milestones:%d parts:%+v mass:%+v career:%q}, want {seq:%d engine:%+v milestones:%d parts:%+v mass:%+v career:%q}",
			ids.String(flight), got.StartedSeq, got.EngineCount, got.Milestones, got.PartCount, got.LaunchMassKg, got.Career,
			want.StartedSeq, want.EngineCount, want.Milestones, want.PartCount, want.LaunchMassKg, want.Career)
	}
	if got.FirstOrbitSeq != want.FirstOrbitSeq {
		t.Errorf("Flight(%s) first orbit seq = %d, want %d", ids.String(flight), got.FirstOrbitSeq, want.FirstOrbitSeq)
	}
}
