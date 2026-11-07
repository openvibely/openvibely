param([string]$FilePath, [int]$TargetX, [int]$TargetY)
$ErrorActionPreference = 'Stop'
Add-Type -ReferencedAssemblies System.Windows.Forms,System.Drawing -TypeDefinition @'
using System;
using System.Drawing;
using System.Runtime.InteropServices;
using System.Windows.Forms;

public sealed class NativeFileDropSource : Form {
    [DllImport("user32.dll")] static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] static extern bool GetCursorPos(out Point point);
    [DllImport("user32.dll")] static extern void mouse_event(uint flags, uint x, uint y, uint data, UIntPtr extra);
    [DllImport("user32.dll")] static extern bool SetProcessDPIAware();
    readonly Timer timer = new Timer();
    readonly string path;
    readonly Point destination;
    Point start;
    int step;
    bool dragging, completed;

    NativeFileDropSource(string file, int x, int y) {
        path = file; destination = new Point(x, y);
        FormBorderStyle = FormBorderStyle.FixedToolWindow;
        StartPosition = FormStartPosition.Manual;
        // Keep the source away from the intended destination.
        Location = new Point(x + 200, y);
        ClientSize = new Size(150, 90);
        TopMost = true;
        Text = "Native file drag source";
        MouseDown += (sender, args) => {
            if (dragging) return;
            dragging = true;
            var data = new DataObject(DataFormats.FileDrop, new string[] { path });
            DoDragDrop(data, DragDropEffects.Copy);
            completed = true;
        };
        Shown += (sender, args) => {
            start = PointToScreen(new Point(60, 45));
            timer.Start();
        };
        timer.Interval = 50;
        timer.Tick += (sender, args) => {
            ++step;
            if (step == 1) {
                SetCursorPos(start.X, start.Y);
                mouse_event(0x0002, 0, 0, 0, UIntPtr.Zero);
            } else if (step <= 21) {
                int n = step - 1;
                SetCursorPos(start.X + (destination.X-start.X)*n/20, start.Y + (destination.Y-start.Y)*n/20);
            } else if (step == 30) {
                mouse_event(0x0004, 0, 0, 0, UIntPtr.Zero);
            } else if (step >= 45) {
                timer.Stop();
                Close();
            }
        };
    }
    public static void Run(string file, int x, int y) {
        SetProcessDPIAware();
        Point original;
        GetCursorPos(out original);
        try {
            using (var form = new NativeFileDropSource(file, x, y)) {
                Application.Run(form);
                if (!form.dragging || !form.completed) throw new Exception("Native OLE drag did not complete");
            }
        } finally {
            mouse_event(0x0004, 0, 0, 0, UIntPtr.Zero);
            SetCursorPos(original.X, original.Y);
        }
    }
}
'@
[NativeFileDropSource]::Run($FilePath, $TargetX, $TargetY)
