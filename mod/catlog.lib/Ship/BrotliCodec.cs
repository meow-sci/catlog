using System;
using System.IO;
using System.IO.Compression;

namespace MeowSci.Catlog.Lib.Ship;

/// <summary>
/// Brotli framing for the ingest body (D18: NDJSON + <c>Content-Encoding: br</c>).
/// </summary>
/// <remarks>
/// <see cref="CompressionLevel.Optimal"/> rather than <see cref="CompressionLevel.SmallestSize"/>:
/// on .NET the latter is Brotli quality 11, which is roughly two orders of magnitude slower to
/// encode for a few percent of size on NDJSON that is already highly repetitive. This runs on a
/// worker in a game process, so encode time matters more than the last few kilobytes.
/// </remarks>
public static class BrotliCodec
{
    /// <summary>Compresses a body.</summary>
    /// <param name="data">The uncompressed NDJSON bytes.</param>
    /// <returns>The Brotli-compressed bytes, exactly as they will be sent.</returns>
    public static byte[] Compress(byte[] data)
    {
        using var output = new MemoryStream();
        using (var brotli = new BrotliStream(output, CompressionLevel.Optimal, leaveOpen: true))
            brotli.Write(data, 0, data.Length);
        return output.ToArray();
    }

    /// <summary>
    /// Decompresses a body, refusing to expand past <paramref name="maxBytes"/>. Used by the
    /// simulator and the tests to check what was actually shipped; the mod never decompresses a
    /// request it built.
    /// </summary>
    /// <param name="data">The Brotli-compressed bytes.</param>
    /// <param name="maxBytes">Hard cap on the decompressed size.</param>
    /// <returns>The decompressed bytes.</returns>
    /// <exception cref="InvalidDataException">The stream expands past <paramref name="maxBytes"/> or is malformed.</exception>
    public static byte[] Decompress(byte[] data, int maxBytes = Wire.MaxDecompressedBytes)
    {
        using var input = new MemoryStream(data, writable: false);
        using var brotli = new BrotliStream(input, CompressionMode.Decompress);
        using var output = new MemoryStream();

        byte[] buffer = new byte[81920];
        int total = 0;
        while (true)
        {
            int read;
            try
            {
                read = brotli.Read(buffer, 0, buffer.Length);
            }
            catch (InvalidOperationException ex)
            {
                // The BCL decoder reports corrupt input as InvalidOperationException; normalize it
                // so callers have one exception type to handle.
                throw new InvalidDataException("The Brotli stream is malformed.", ex);
            }

            if (read <= 0)
                break;

            total += read;
            if (total > maxBytes)
                throw new InvalidDataException($"Decompressed body exceeds {maxBytes} bytes.");
            output.Write(buffer, 0, read);
        }

        return output.ToArray();
    }
}
