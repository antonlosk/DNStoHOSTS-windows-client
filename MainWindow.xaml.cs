using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;

namespace DNStoHOSTS
{
    public partial class MainWindow : Window
    {
        private const string InputFile = "input.txt";
        private const string SettingsFile = "settings.txt";
        private const string OutputFile = "output.txt";
        private CancellationTokenSource _cts;
        private static readonly HttpClient _client = new HttpClient(new HttpClientHandler { UseProxy = false });

        public MainWindow()
        {
            ServicePointManager.SecurityProtocol = SecurityProtocolType.Tls12 | (SecurityProtocolType)12288;
            InitializeComponent();
            if (!File.Exists(InputFile)) File.WriteAllText(InputFile, "google.com\n");
            if (!File.Exists(SettingsFile)) File.WriteAllText(SettingsFile, "server=dns.google\nport=443\nipv4=true\nipv6=false\n");
        }

        private void Log(string m) => Dispatcher.Invoke(() => { LogTextBox.AppendText($"[{DateTime.Now:HH:mm:ss}] {m}\r\n"); LogTextBox.ScrollToEnd(); });

        private async void BtnStart_Click(object sender, RoutedEventArgs e)
        {
            if (_cts != null) return;
            _cts = new CancellationTokenSource();
            try { await Task.Run(() => MainWork(_cts.Token)); }
            catch (Exception ex) { Log("Error: " + ex.Message); }
            finally { _cts.Dispose(); _cts = null; }
        }

        private async Task MainWork(CancellationToken ct)
        {
            Log("Starting...");
            var cfg = ParseCfg();
            var lines = File.Exists(InputFile) ? File.ReadAllLines(InputFile) : new string[0];
            var res = new List<string>();

            foreach (var line in lines)
            {
                if (ct.IsCancellationRequested) break;
                string d = line.Trim();
                if (string.IsNullOrEmpty(d) || d.StartsWith("#")) { res.Add(line); continue; }

                Log("Resolving: " + d);
                var ips = new List<string>();
                if (cfg.v4) ips.AddRange(await GetIP(d, 1, cfg, ct));
                if (cfg.v6) ips.AddRange(await GetIP(d, 28, cfg, ct));

                if (ips.Any()) {
                    foreach (var ip in ips.Distinct()) { Log(" -> " + ip); res.Add($"{ip} {d}"); }
                } else { Log(" [!] Fail"); res.Add("# Fail: " + d); }
            }
            File.WriteAllLines(OutputFile, res);
            Log("Done.");
        }

        private async Task<List<string>> GetIP(string d, ushort t, dynamic c, CancellationToken ct)
        {
            var ret = new List<string>();
            try {
                var ms = new MemoryStream();
                ms.Write(new byte[] { 0, 0, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0 }, 0, 12);
                foreach (var p in d.Split('.')) {
                    byte[] b = Encoding.ASCII.GetBytes(p);
                    ms.WriteByte((byte)b.Length); ms.Write(b, 0, b.Length);
                }
                ms.Write(new byte[] { 0, 0, (byte)t, 0, 1 }, 0, 5);

                var req = new HttpRequestMessage(HttpMethod.Post, $"https://{c.s}:{c.p}/dns-query");
                req.Content = new ByteArrayContent(ms.ToArray());
                req.Content.Headers.ContentType = new MediaTypeHeaderValue("application/dns-message");
                req.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/dns-message"));

                var resp = await _client.SendAsync(req, ct);
                if (resp.IsSuccessStatusCode) {
                    byte[] r = await resp.Content.ReadAsByteArrayAsync();
                    int pos = 12;
                    int qd = (r[4] << 8) | r[5];
                    int an = (r[6] << 8) | r[7];
                    for (int i = 0; i < qd; i++) { Skip(r, ref pos); pos += 4; }
                    for (int i = 0; i < an; i++) {
                        Skip(r, ref pos);
                        ushort type = (ushort)((r[pos] << 8) | r[pos + 1]); pos += 8;
                        ushort len = (ushort)((r[pos] << 8) | r[pos + 1]); pos += 2;
                        if (type == t) {
                            if (t == 1 && len == 4) ret.Add($"{r[pos]}.{r[pos+1]}.{r[pos+2]}.{r[pos+3]}");
                            if (t == 28 && len == 16) {
                                byte[] ipb = new byte[16]; Array.Copy(r, pos, ipb, 0, 16);
                                ret.Add(new IPAddress(ipb).ToString());
                            }
                        }
                        pos += len;
                    }
                }
            } catch { }
            return ret;
        }

        private void Skip(byte[] r, ref int p) {
            while (p < r.Length) {
                int b = r[p];
                if (b == 0) { p++; break; }
                if ((b & 0xc0) == 0xc0) { p += 2; break; }
                p += b + 1;
            }
        }

        private dynamic ParseCfg() {
            string s = "dns.google"; int p = 443; bool v4 = true, v6 = false;
            if (File.Exists(SettingsFile))
                foreach (var l in File.ReadAllLines(SettingsFile)) {
                    var x = l.Split('='); if (x.Length != 2) continue;
                    var k = x[0].Trim().ToLower(); var v = x[1].Trim().ToLower();
                    if (k == "server") s = v; else if (k == "port") int.TryParse(v, out p);
                    else if (k == "ipv4") v4 = v == "true"; else if (k == "ipv6") v6 = v == "true";
                }
            return new { s, p, v4, v6 };
        }

        private void BtnStop_Click(object sender, System.Windows.RoutedEventArgs e) => _cts?.Cancel();
        private void BtnClear_Click(object sender, System.Windows.RoutedEventArgs e) => LogTextBox.Clear();
        private void BtnTheme_Click(object sender, System.Windows.RoutedEventArgs e) { }
        private void BtnFile_Click(object sender, System.Windows.RoutedEventArgs e) { }
    }
}
