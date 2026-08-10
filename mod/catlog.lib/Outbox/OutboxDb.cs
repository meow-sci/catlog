using System;
using System.Collections.Generic;
using System.Data;
using System.Globalization;
using System.IO;
using System.Text;
using Microsoft.Data.Sqlite;
using MeowSci.Catlog.Lib.Events;

namespace MeowSci.Catlog.Lib.Outbox;

/// <summary>A drained batch of NDJSON lines, ready to compress and ship.</summary>
/// <param name="LastRowId">The highest outbox row id in the batch; pass it to <see cref="OutboxDb.MarkShipped"/> on a 200.</param>
/// <param name="Lines">The NDJSON lines, oldest first (§4.3 requires append order).</param>
/// <param name="TotalBytes">Total UTF-8 size of the lines, excluding separators.</param>
public sealed record OutboxBatch(long LastRowId, IReadOnlyList<string> Lines, int TotalBytes)
{
    /// <summary>The empty batch.</summary>
    public static OutboxBatch Empty { get; } = new(0, [], 0);

    /// <summary>How many events are in the batch.</summary>
    public int Count => Lines.Count;

    /// <summary>True when there is nothing to ship.</summary>
    public bool IsEmpty => Lines.Count == 0;

    /// <summary>
    /// The batch as NDJSON: one envelope per line, LF-separated, with a trailing LF. This is the
    /// exact byte sequence that gets Brotli-compressed and hashed into the proof's <c>bh</c>.
    /// </summary>
    /// <returns>The UTF-8 NDJSON bytes.</returns>
    public byte[] ToNdjson()
    {
        var sb = new StringBuilder(TotalBytes + Lines.Count);
        foreach (string line in Lines)
        {
            sb.Append(line);
            sb.Append('\n');
        }

        return Encoding.UTF8.GetBytes(sb.ToString());
    }
}

/// <summary>
/// The mod's private write-ahead spool: events are durably appended locally, then drained in
/// order by the shipper and deleted only once the server has accepted them.
/// </summary>
/// <remarks>
/// <para>
/// Uses <c>Microsoft.Data.Sqlite</c> (D4: no Turso C# SDK exists, and this is a private spool, not
/// the server DB). Unlike unscience's <c>EventDatabase</c> — which has no WAL, no transactions, no
/// prune and no recovery story — this one is built for the crash case: the game can be killed at
/// any instant, and every event that was appended before the kill must still be there, exactly
/// once, in order.
/// </para>
/// <para>
/// Methods throw <see cref="SqliteException"/> on a genuine storage failure. Callers latch the
/// outbox subsystem dead (see <see cref="Util.SubsystemHealth"/>) rather than retrying — a
/// permanently failing outbox must be visible, not silently swallowed.
/// </para>
/// </remarks>
public sealed class OutboxDb : IDisposable
{
    /// <summary>Current DDL version. A mismatch is a hard error; the file is not migrated in place.</summary>
    public const int SchemaVersion = 1;

    private static readonly string[] Ddl =
    [
        """
        CREATE TABLE IF NOT EXISTS outbox_event (
            id         INTEGER PRIMARY KEY,
            event_id   TEXT    NOT NULL UNIQUE,
            kind       INTEGER NOT NULL,
            created_ms INTEGER NOT NULL,
            body       TEXT    NOT NULL
        )
        """,
        "CREATE INDEX IF NOT EXISTS idx_outbox_kind_id ON outbox_event(kind, id)",
        "CREATE TABLE IF NOT EXISTS shipper_state (k TEXT PRIMARY KEY, v TEXT NOT NULL)",
        "CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)",
    ];

    private readonly SqliteConnection _connection;

    private OutboxDb(SqliteConnection connection, string path)
    {
        _connection = connection;
        Path = path;
    }

    /// <summary>The database file path.</summary>
    public string Path { get; }

    /// <summary>
    /// Opens (creating if necessary) the outbox at <paramref name="path"/>.
    /// </summary>
    /// <param name="path">Database file path. Parent directories are created.</param>
    /// <returns>The open outbox.</returns>
    /// <exception cref="SqliteException">The file could not be opened or the schema could not be applied.</exception>
    /// <exception cref="InvalidOperationException">The file carries a schema version this build does not understand.</exception>
    public static OutboxDb Open(string path)
    {
        // Required in plugin/mod environments: Microsoft.Data.Sqlite normally initializes
        // SQLitePCLRaw from a module initializer that StarMap's assembly load context does not
        // run, and without this the first query throws TypeInitializationException from deep
        // inside the provider. Init() is idempotent.
        SQLitePCL.Batteries_V2.Init();

        string? directory = System.IO.Path.GetDirectoryName(System.IO.Path.GetFullPath(path));
        if (!string.IsNullOrEmpty(directory))
            Directory.CreateDirectory(directory);

        var connection = new SqliteConnection(new SqliteConnectionStringBuilder
        {
            DataSource = path,
            // Pooling would keep the file handle alive past Dispose, which breaks the
            // "reopen after a crash" path and leaves -wal files locked on Windows.
            Pooling = false,
        }.ToString());
        connection.Open();

        // WAL survives a game crash mid-write; NORMAL is the durable-enough/fast pairing with it;
        // busy_timeout covers the shipper and the worker racing on the same file.
        Exec(connection, "PRAGMA journal_mode=WAL");
        Exec(connection, "PRAGMA synchronous=NORMAL");
        Exec(connection, "PRAGMA busy_timeout=3000");
        Exec(connection, "PRAGMA foreign_keys=ON");

        foreach (string statement in Ddl)
            Exec(connection, statement);

        Exec(connection, $"INSERT OR IGNORE INTO schema_version(version) VALUES ({SchemaVersion})");

        var db = new OutboxDb(connection, path);
        int found = db.ReadSchemaVersion();
        if (found != SchemaVersion)
        {
            db.Dispose();
            throw new InvalidOperationException(
                $"Outbox '{path}' has schema version {found}; this build understands {SchemaVersion}.");
        }

        return db;
    }

    /// <summary>How many events are waiting to be shipped.</summary>
    public long PendingCount => ScalarLong("SELECT COUNT(*) FROM outbox_event");

    /// <summary>The <c>created_ms</c> of the oldest pending event, or null when the outbox is empty.</summary>
    public long? OldestCreatedMs
    {
        get
        {
            using SqliteCommand cmd = _connection.CreateCommand();
            cmd.CommandText = "SELECT created_ms FROM outbox_event ORDER BY id LIMIT 1";
            object? value = cmd.ExecuteScalar();
            return value is null or DBNull ? null : Convert.ToInt64(value, CultureInfo.InvariantCulture);
        }
    }

    /// <summary>Total UTF-8 size of every pending event body, in bytes.</summary>
    public long TotalBytes
        => ScalarLong("SELECT COALESCE(SUM(LENGTH(CAST(body AS BLOB))), 0) FROM outbox_event");

    /// <summary>
    /// Appends events in one transaction with one prepared statement. Duplicate <c>event_id</c>s
    /// are ignored, so a retried append is idempotent.
    /// </summary>
    /// <param name="envelopes">The events, in emission order.</param>
    /// <returns>How many rows were actually inserted.</returns>
    public int Append(IReadOnlyList<EventEnvelope> envelopes)
    {
        if (envelopes.Count == 0)
            return 0;

        using SqliteTransaction txn = _connection.BeginTransaction();
        int inserted = Append(envelopes, txn);
        txn.Commit();
        return inserted;
    }

    /// <summary>
    /// Atomically appends an ordered event batch and advances one durable state value. The state
    /// write happens after every insert, so any failure rolls both parts back.
    /// </summary>
    public int AppendAndSetState(
        IReadOnlyList<EventEnvelope> envelopes, string key, string value)
    {
        using SqliteTransaction txn = _connection.BeginTransaction();
        int inserted = Append(envelopes, txn);

        using SqliteCommand state = _connection.CreateCommand();
        state.Transaction = txn;
        state.CommandText =
            "INSERT INTO shipper_state(k, v) VALUES ($k, $v) ON CONFLICT(k) DO UPDATE SET v = excluded.v";
        state.Parameters.AddWithValue("$k", key);
        state.Parameters.AddWithValue("$v", value);
        state.ExecuteNonQuery();

        txn.Commit();
        return inserted;
    }

    private int Append(IReadOnlyList<EventEnvelope> envelopes, SqliteTransaction txn)
    {
        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.Transaction = txn;
        cmd.CommandText =
            "INSERT OR IGNORE INTO outbox_event(event_id, kind, created_ms, body) VALUES ($id, $kind, $ms, $body)";
        SqliteParameter id = cmd.Parameters.Add("$id", SqliteType.Text);
        SqliteParameter kind = cmd.Parameters.Add("$kind", SqliteType.Integer);
        SqliteParameter ms = cmd.Parameters.Add("$ms", SqliteType.Integer);
        SqliteParameter body = cmd.Parameters.Add("$body", SqliteType.Text);

        int inserted = 0;
        foreach (EventEnvelope envelope in envelopes)
        {
            id.Value = envelope.Id;
            kind.Value = EventTypes.KindOf(envelope.Type);
            ms.Value = envelope.WallT;
            body.Value = envelope.ToNdjsonLine();
            inserted += cmd.ExecuteNonQuery();
        }

        return inserted;
    }

    /// <summary>
    /// Reads the oldest pending events without removing them, stopping at whichever cap is hit
    /// first. Nothing is deleted until <see cref="MarkShipped"/>, which is what makes a crash
    /// mid-ship a re-ship (the server dedups) rather than a data loss.
    /// </summary>
    /// <param name="maxEvents">Event-count cap; clamped to <see cref="Wire.MaxEventsPerBatch"/>.</param>
    /// <param name="maxBytes">Uncompressed-size cap; clamped to <see cref="Wire.MaxDecompressedBytes"/>.</param>
    /// <returns>The batch, possibly <see cref="OutboxBatch.Empty"/>.</returns>
    public OutboxBatch NextBatch(int maxEvents = Wire.DefaultBatchEventCap, int maxBytes = Wire.MaxDecompressedBytes)
    {
        maxEvents = Math.Clamp(maxEvents, 1, Wire.MaxEventsPerBatch);
        maxBytes = Math.Clamp(maxBytes, 1, Wire.MaxDecompressedBytes);

        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText = "SELECT id, body FROM outbox_event ORDER BY id LIMIT $limit";
        cmd.Parameters.AddWithValue("$limit", maxEvents);

        var lines = new List<string>(Math.Min(maxEvents, 256));
        long lastRowId = 0;
        int totalBytes = 0;

        using SqliteDataReader reader = cmd.ExecuteReader();
        while (reader.Read())
        {
            long rowId = reader.GetInt64(0);
            string line = reader.GetString(1);
            int size = Encoding.UTF8.GetByteCount(line);

            // A single event over the per-line cap can never be shipped. Take it anyway when it
            // is the first in the batch so the shipper sees it and can fail loudly, rather than
            // silently wedging the whole outbox behind it.
            if (lines.Count > 0 && totalBytes + size + 1 > maxBytes)
                break;

            lines.Add(line);
            totalBytes += size;
            lastRowId = rowId;
        }

        return lines.Count == 0 ? OutboxBatch.Empty : new OutboxBatch(lastRowId, lines, totalBytes);
    }

    /// <summary>Deletes every row up to and including <paramref name="lastRowId"/>. Call only after a 200.</summary>
    /// <param name="lastRowId">The batch's highest row id.</param>
    /// <returns>How many rows were deleted.</returns>
    public int MarkShipped(long lastRowId)
    {
        if (lastRowId <= 0)
            return 0;

        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText = "DELETE FROM outbox_event WHERE id <= $id";
        cmd.Parameters.AddWithValue("$id", lastRowId);
        return cmd.ExecuteNonQuery();
    }

    /// <summary>
    /// Enforces the outbox size cap by dropping the <b>oldest passive rows first</b>
    /// (<c>kind = 0</c>). Scoring events (<c>kind = 1</c>) are never dropped: losing a
    /// <c>telemetry.window</c> costs resolution on a graph, losing a <c>vehicle.impact</c> costs a
    /// leaderboard entry.
    /// </summary>
    /// <remarks>
    /// The running total is measured once and then <b>decremented by what each batch actually
    /// removed</b>, rather than re-measured per batch. <see cref="TotalBytes"/> is a full-table
    /// <c>SUM(LENGTH(...))</c>, so re-asking it after every 128-row delete made the whole prune
    /// quadratic in the size of the outbox — the one place where a cap that has just been exceeded
    /// by a long session is exactly when the table is biggest. The shipper may delete rows on a
    /// <c>200</c> while this runs, which can only make the true total smaller than the running one;
    /// the cost is at worst a few extra passive rows dropped on that pass, and passive rows are the
    /// ones this method exists to drop.
    /// </remarks>
    /// <param name="capBytes">Target size in bytes.</param>
    /// <returns>How many rows were dropped.</returns>
    public int Prune(long capBytes)
    {
        if (capBytes <= 0)
            return 0;

        long total = TotalBytes;
        int dropped = 0;
        while (total > capBytes)
        {
            // Measure and bound the batch in one indexed pass (idx_outbox_kind_id covers both), so
            // the delete below is a range on the same index rather than a subquery re-run.
            (long batchBytes, long lastId) = OldestDroppable(128);
            if (lastId < 0)
                break; // Nothing droppable left: the outbox is all scoring events.

            using SqliteCommand cmd = _connection.CreateCommand();
            cmd.CommandText = "DELETE FROM outbox_event WHERE kind = 0 AND id <= $id";
            cmd.Parameters.AddWithValue("$id", lastId);
            int deleted = cmd.ExecuteNonQuery();
            if (deleted == 0)
                break;

            dropped += deleted;
            total -= batchBytes;
        }

        return dropped;
    }

    /// <summary>
    /// The size and highest id of the oldest droppable (<c>kind = 0</c>) rows, capped at
    /// <paramref name="limit"/> rows. <c>lastId</c> is <c>-1</c> when there are none.
    /// </summary>
    /// <param name="limit">How many rows the batch may cover.</param>
    /// <returns>The batch's byte total and its highest row id.</returns>
    private (long Bytes, long LastId) OldestDroppable(int limit)
    {
        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText =
            """
            SELECT COALESCE(SUM(LENGTH(CAST(body AS BLOB))), 0), COALESCE(MAX(id), -1) FROM (
                SELECT id, body FROM outbox_event WHERE kind = 0 ORDER BY id LIMIT $limit
            )
            """;
        cmd.Parameters.AddWithValue("$limit", limit);

        using SqliteDataReader reader = cmd.ExecuteReader();
        return reader.Read() ? (reader.GetInt64(0), reader.GetInt64(1)) : (0, -1);
    }

    /// <summary>Reads a <c>shipper_state</c> value.</summary>
    /// <param name="key">One of <see cref="Wire.StateKeys"/>.</param>
    /// <returns>The value, or null when unset.</returns>
    public string? GetState(string key)
    {
        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText = "SELECT v FROM shipper_state WHERE k = $k";
        cmd.Parameters.AddWithValue("$k", key);
        return cmd.ExecuteScalar() as string;
    }

    /// <summary>Writes a <c>shipper_state</c> value.</summary>
    /// <param name="key">One of <see cref="Wire.StateKeys"/>.</param>
    /// <param name="value">The value.</param>
    public void SetState(string key, string value)
    {
        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText =
            "INSERT INTO shipper_state(k, v) VALUES ($k, $v) ON CONFLICT(k) DO UPDATE SET v = excluded.v";
        cmd.Parameters.AddWithValue("$k", key);
        cmd.Parameters.AddWithValue("$v", value);
        cmd.ExecuteNonQuery();
    }

    /// <summary>Removes a <c>shipper_state</c> key.</summary>
    /// <param name="key">One of <see cref="Wire.StateKeys"/>.</param>
    public void ClearState(string key)
    {
        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText = "DELETE FROM shipper_state WHERE k = $k";
        cmd.Parameters.AddWithValue("$k", key);
        cmd.ExecuteNonQuery();
    }

    /// <summary>Closes the connection. Safe to call more than once.</summary>
    public void Dispose()
    {
        try
        {
            if (_connection.State != ConnectionState.Closed)
                _connection.Close();
            _connection.Dispose();
        }
        catch (SqliteException)
        {
            // Closing must never throw at a shutdown boundary.
        }
    }

    private int ReadSchemaVersion()
    {
        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText = "SELECT MAX(version) FROM schema_version";
        object? value = cmd.ExecuteScalar();
        return value is null or DBNull ? 0 : Convert.ToInt32(value, CultureInfo.InvariantCulture);
    }

    private long ScalarLong(string sql)
    {
        using SqliteCommand cmd = _connection.CreateCommand();
        cmd.CommandText = sql;
        object? value = cmd.ExecuteScalar();
        return value is null or DBNull ? 0 : Convert.ToInt64(value, CultureInfo.InvariantCulture);
    }

    private static void Exec(SqliteConnection connection, string sql)
    {
        using SqliteCommand cmd = connection.CreateCommand();
        cmd.CommandText = sql;
        cmd.ExecuteNonQuery();
    }
}
