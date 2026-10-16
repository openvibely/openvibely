package filecontent

// Windows Shell uses the installed image codecs, as Electron's native thumbnail
// API does. THUMBNAILONLY prevents a file icon from masquerading as a preview.
// Paths are passed as environment data, never interpolated into PowerShell code.
const windowsImagePreviewScript = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
Add-Type -ReferencedAssemblies System.Drawing -TypeDefinition @'
using System;
using System.Drawing;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;
public static class OpenVibelyImagePreview {
    [StructLayout(LayoutKind.Sequential)] struct Size { public int cx, cy; }
    [ComImport, Guid("bcc18b79-ba16-442f-80c4-8a59c30c463b"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface ImageFactory {
        [PreserveSig] int GetImage(Size size, int flags, out IntPtr bitmap);
    }
    [DllImport("shell32.dll", CharSet=CharSet.Unicode, PreserveSig=true)]
    static extern int SHCreateItemFromParsingName(string path, IntPtr context, ref Guid iid, out ImageFactory factory);
    [DllImport("gdi32.dll")] static extern bool DeleteObject(IntPtr handle);
    public static void Convert(string input, string output) {
        ImageFactory factory = null;
        IntPtr handle = IntPtr.Zero;
        try {
            Guid iid = typeof(ImageFactory).GUID;
            Marshal.ThrowExceptionForHR(SHCreateItemFromParsingName(input, IntPtr.Zero, ref iid, out factory));
            Marshal.ThrowExceptionForHR(factory.GetImage(new Size { cx=1024, cy=1024 }, 8, out handle));
            if (handle == IntPtr.Zero) throw new InvalidOperationException("No thumbnail available");
            using (Bitmap image = Image.FromHbitmap(handle)) {
                if (image.Width < 1 || image.Height < 1 || image.Width > 1024 || image.Height > 1024)
                    throw new InvalidOperationException("Invalid thumbnail dimensions");
                ImageCodecInfo jpeg = Array.Find(ImageCodecInfo.GetImageEncoders(), e => e.MimeType == "image/jpeg");
                using (EncoderParameters parameters = new EncoderParameters(1)) {
                    parameters.Param[0] = new EncoderParameter(System.Drawing.Imaging.Encoder.Quality, 85L);
                    image.Save(output, jpeg, parameters);
                }
            }
        } finally {
            if (handle != IntPtr.Zero) DeleteObject(handle);
            if (factory != null) Marshal.ReleaseComObject(factory);
        }
    }
}
'@
[OpenVibelyImagePreview]::Convert($env:OPENVIBELY_PREVIEW_INPUT, $env:OPENVIBELY_PREVIEW_OUTPUT)
`
