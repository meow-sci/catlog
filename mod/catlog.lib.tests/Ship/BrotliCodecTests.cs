using System.IO;
using System.Text;
using MeowSci.Catlog.Lib.Ship;
using Xunit;

namespace MeowSci.Catlog.Lib.Tests.Ship;

/// <summary>INITIAL_IMPL_PLAN D18 / §4.3: Brotli framing of the NDJSON body.</summary>
public sealed class BrotliCodecTests
{
    [Fact]
    public void RoundTripsExactBytes()
    {
        byte[] original = Encoding.UTF8.GetBytes("{\"a\":1}\n{\"a\":2}\n");

        byte[] roundTripped = BrotliCodec.Decompress(BrotliCodec.Compress(original));

        Assert.Equal(original, roundTripped);
    }

    [Fact]
    public void CompressesRepetitiveNdjsonWell()
    {
        var sb = new StringBuilder();
        for (int i = 0; i < 500; i++)
            sb.Append("{\"id\":\"01J9V5M3E8Z0FAKEULID26CHR\",\"type\":\"vehicle.staging\",\"ver\":1}\n");
        byte[] original = Encoding.UTF8.GetBytes(sb.ToString());

        byte[] compressed = BrotliCodec.Compress(original);

        Assert.True(compressed.Length < original.Length / 10,
            $"NDJSON is highly repetitive; got {compressed.Length} from {original.Length}");
    }

    [Fact]
    public void RoundTripsAnEmptyBody()
    {
        Assert.Empty(BrotliCodec.Decompress(BrotliCodec.Compress([])));
    }

    [Fact]
    public void DecompressRefusesToExpandPastTheCap()
    {
        byte[] compressed = BrotliCodec.Compress(new byte[1_000_000]);

        Assert.Throws<InvalidDataException>(() => BrotliCodec.Decompress(compressed, maxBytes: 1_024));
    }

    [Fact]
    public void DecompressRejectsGarbage()
    {
        Assert.ThrowsAny<InvalidDataException>(
            () => BrotliCodec.Decompress([0xFF, 0xFE, 0xFD, 0x00, 0x01, 0x02]));
    }
}
