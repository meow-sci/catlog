using System;
using System.Collections.Generic;
using MeowSci.Catlog.Lib.Events;
using MeowSci.Catlog.Lib.Outbox;

namespace MeowSci.Catlog.Lib.Detect;

/// <summary>
/// KSA-free coordinator for one system/session boundary. It keeps the durable marker decision and
/// the pipeline's ordered envelope production inside one directly testable operation.
/// </summary>
public static class SystemBoundaryDelivery
{
    /// <summary>Produces and durably appends one boundary, returning inserted row count.</summary>
    public static int Append(EventPipeline pipeline, OutboxDb outbox, SessionLoadedSignal loaded)
    {
        if (loaded.System is null)
            return 0;

        string career = string.IsNullOrEmpty(loaded.CareerId) ? pipeline.CareerId : loaded.CareerId;
        string marker = Wire.StateKeys.Survey(career, loaded.System.SystemId);
        bool complete = pipeline.Types.IsEnabled(EventTypes.SystemBody)
            && loaded.System.BodyCount <= Wire.MaxSystemBodies
            && loaded.System.HasValidRequiredNumerics;
        bool includeBodies = complete && outbox.GetState(marker) is null;
        SessionLoadedSignal prepared = loaded with
        {
            IncludeSystemBodies = includeBodies,
            SystemComplete = complete && includeBodies,
        };
        IReadOnlyList<EventEnvelope> envelopes = pipeline.ProcessSignal(prepared);
        return includeBodies
            ? outbox.AppendAndSetState(envelopes, marker, "1")
            : outbox.Append(envelopes);
    }
}
