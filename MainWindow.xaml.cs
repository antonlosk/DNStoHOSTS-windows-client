using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Media;

namespace DNStoHOSTS
{
    public partial class MainWindow : Window
    {
        private const string InputFile = "input.txt";
        private const string SettingsFile = "settings.txt";
        private const string OutputFile = "output.txt";

        private bool _isDarkTheme = true;
        private CancellationTokenSource _cts;
        private static readonly HttpClient _httpClient = new HttpClient();
        private static readonly Random _rnd = new Random();

        public MainWindow()
        {
            InitializeComponent();
            CheckAndCreateDefaultFiles();
        }

        private void CheckAndCreateDefaultFiles()
        {
            try
            {
                if (!File.Exists(InputFile)) File.WriteAllText(InputFile, "# Google\r\ngoogle.com\r\n");
                if (!File.Exists(SettingsFile)) File.WriteAllText(SettingsFile, "server=dns.google\r\nport=443\r\nipv4=true\r\nipv6=false\r\n");
            }
            catch { }
        }

        private void Log(string message)
        {
            Dispatcher.Invoke(() =>
            {
                LogTextBox.AppendText($"[{DateTime.Now:HH:mm:ss}] {message}\r\n");
                LogTextBox.ScrollToEnd();
            });
        }

        private void BtnTheme_Click(object sender, RoutedEventArgs e)
        {
            _isDarkTheme = !_isDarkTheme;
            if (_isDarkTheme) SetColors("#1E1E1E", "#252526", "#D4D4D4", "#333333");
            else SetColors("#F3F3F3", "#FFFFFF", "#000000", "#E1E1E1");
        }

        private void SetColors(string win, string log, string text, string btn)
        {
            Resources["WindowBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(win));
            Resources["LogBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(log));
            Resources["TextColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(text));
            Resources["ButtonBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(btn));
        }

        private void BtnFile_Click(object sender, RoutedEventArgs e)
        {
            if (sender is System.Windows.Controls.Button btn && btn.Tag is string fn && File.Exists(fn))
                Process.Start(new ProcessStartInfo(fn) { UseShellExecute = true });
        }

        private void BtnClear_Click(object sender, RoutedEventArgs e) => LogTextBox.Clear();

        private void BtnStop_Click(object sender, RoutedEventArgs e) => _cts?.Cancel();

        private async void BtnStart_Click(object sender, RoutedEventArgs e)
        {
            if (_cts != null) return;
            _cts = new CancellationTokenSource();
            MainProgress.Foreground = (SolidColorBrush)FindResource("ProgressBlue");

            try { await Task.Run(() => ProcessDomains(_cts.Token)); MainProgress.Foreground = (SolidColorBrush)FindResource("ProgressGreen"); }
            catch (OperationCanceledException) { Log("Stopped."); }
            catch (Exception ex) { Log("Error: " + ex.Message); }
            finally { _cts.Dispose(); _cts = null; }
        }

        private async Task ProcessDomains(CancellationToken token)
        {
            Log("Starting...");
            var settings = ReadSettings();
            if (!File.Exists(InputFile)) return;

            var lines = File.ReadAllLines(InputFile);
            var output = new List<string>();
            var targets = lines.Where(l => !l.TrimStart().StartsWith("#") && !string.IsNullOrWhiteSpace(l)).ToList();

            Dispatcher.Invoke(() => { MainProgress.Maximum = targets.Count; MainProgress.Value = 0; });

            int count = 0;
            foreach (var line in lines)
            {
                token.ThrowIfCancellationRequested();
                if (string.IsNullOrWhiteSpace(line) || line.Trim().StartsWith("#")) { output.Add(line); continue; }

                string domain = line.Trim();
                Log("Resolving: " + domain);
                var ips = new List<string>();
                
                if (settings.ipv4) ips.AddRange(await Resolve(domain, 1, settings, token));
                if (settings.ipv6) ips.AddRange(await Resolve(domain, 28, settings, token));

                if (!ips.Any())
                {
                    Log("  [!] No records found for " + domain);
                    output.Add("# No records: " + domain);
                }
                else 
                {
                    foreach (var ip in ips.Distinct()) 
                    { 
                        Log("  -> " + ip); 
                        output.Add($"{ip} {domain}"); 
                    }
                }

                count++;
                Dispatcher.Invoke(() => MainProgress.Value = count);
            }
            File.WriteAllLines(OutputFile, output);
            Log("Done. Saved to " + OutputFile);
        }

        private async Task<List<string>> Resolve(string domain, ushort type, dynamic s, CancellationToken t)
        {
            var res = new List<string>();
            try
            {
                var req = new HttpRequestMessage(HttpMethod.Post, $"https://{s.server}:{s.port}/dns-query");
                req.Content = new ByteArrayContent(BuildQuery(domain, type));
                req.Content.Headers.ContentType = new MediaTypeHeaderValue("application/dns-message");
                
                var resp = await _httpClient.SendAsync(req, t);
                if (resp.IsSuccessStatusCode) 
                {
                    byte[] data = await resp.Content.ReadAsByteArrayAsync();
                    res.AddRange(ParseResponse(data, type));
                }
            } catch { }
            return res;
        }

        private byte[] BuildQuery(string d, ushort t)
        {
            var ms = new MemoryStream();
            // Header: ID(2), Flags(2), QD(2), AN(2), NS(2), AR(2)
            ms.Write(new byte[] { (byte)_rnd.Next(256), (byte)_rnd.Next(256), 1, 0, 0, 1, 0, 0, 0, 0, 0, 0 }, 0, 12);
            // Question: Name, Type(2), Class(2)
            foreach (var p in d.Split('.')) 
            { 
                ms.WriteByte((byte)p.Length); 
                var b = Encoding.ASCII.GetBytes(p); 
                ms.Write(b, 0, b.Length); 
            }
            ms.Write(new byte[] { 0, (byte)(t >> 8), (byte)(t & 0xff), 0, 1 }, 0, 5);
            return ms.ToArray();
        }

        private List<string> ParseResponse(byte[] r, ushort requestedType)
        {
            var ips = new List<string>();
            try 
            {
                int off = 12; // Skip header
                int qdc = (r[4] << 8) | r[5]; 
                int anc = (r[6] << 8) | r[7];

                // Skip Question Section
                for (int i = 0; i < qdc; i++) 
                {
                    SkipDnsName(r, ref off);
                    off += 4; // Type + Class
                }

                // Parse Answer Section
                for (int i = 0; i < anc; i++) 
                {
                    SkipDnsName(r, ref off);
                    ushort type = (ushort)((r[off] << 8) | r[off + 1]); 
                    off += 8; // Type(2), Class(2), TTL(4)
                    ushort len = (ushort)((r[off] << 8) | r[off + 1]); 
                    off += 2;

                    if (type == requestedType) 
                    {
                        if (type == 1 && len == 4) // IPv4
                            ips.Add($"{r[off]}.{r[off+1]}.{r[off+2]}.{r[off+3]}");
                        else if (type == 28 && len == 16) // IPv6
                        { 
                            byte[] b = new byte[16]; 
                            Array.Copy(r, off, b, 0, 16); 
                            ips.Add(new System.Net.IPAddress(b).ToString()); 
                        }
                    }
                    off += len;
                }
            } catch { }
            return ips;
        }

        private void SkipDnsName(byte[] r, ref int off)
        {
            while (off < r.Length)
            {
                byte len = r[off];
                if (len == 0) { off++; break; }
                if ((len & 0xc0) == 0xc0) { off += 2; break; } // Handle pointers
                off += len + 1;
            }
        }

        private dynamic ReadSettings()
        {
            string s = "dns.google"; int p = 443; bool v4 = true, v6 = false;
            if (File.Exists(SettingsFile))
                foreach (var l in File.ReadAllLines(SettingsFile)) {
                    var x = l.Split('='); if (x.Length != 2) continue;
                    var k = x[0].Trim().ToLower(); var v = x[1].Trim().ToLower();
                    if (k == "server") s = v; else if (k == "port") int.TryParse(v, out p);
                    else if (k == "ipv4") v4 = v == "true"; else if (k == "ipv6") v6 = v == "true";
                }
            return new { server = s, port = p, ipv4 = v4, ipv6 = v6 };
        }
    }
}
