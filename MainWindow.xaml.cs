using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
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

        // Отключаем прокси для ускорения работы и избежания подвисаний
        private static readonly HttpClient _httpClient = new HttpClient(new HttpClientHandler()
        {
            UseProxy = false,
            Proxy = null
        });

        public MainWindow()
        {
            // КРИТИЧЕСКИЙ ФИКС ДЛЯ .NET 4.8: Включаем современные протоколы шифрования
            // Без этого dns.google и другие часто выдают Timeout
            ServicePointManager.SecurityProtocol = SecurityProtocolType.Tls12 | (SecurityProtocolType)12288; // 12288 = TLS 1.3
            ServicePointManager.Expect100Continue = false;
            ServicePointManager.DefaultConnectionLimit = 20;

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

            try 
            { 
                await Task.Run(() => ProcessDomains(_cts.Token)); 
                MainProgress.Foreground = (SolidColorBrush)FindResource("ProgressGreen"); 
            }
            catch (OperationCanceledException) { Log("Stopped by user."); }
            catch (Exception ex) { Log("Critical Error: " + ex.Message); }
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
                var errors = new List<string>();
                
                if (settings.ipv4) ips.AddRange(await ResolveDoH(domain, 1, settings, token, errors));
                if (settings.ipv6) ips.AddRange(await ResolveDoH(domain, 28, settings, token, errors));

                if (!ips.Any())
                {
                    foreach(var err in errors.Distinct()) Log($"  [!] {err}");
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
            Log("Done. Results saved.");
        }

        private async Task<List<string>> ResolveDoH(string domain, ushort type, dynamic s, CancellationToken t, List<string> errorList)
        {
            var res = new List<string>();
            string typeLabel = (type == 1) ? "IPv4" : "IPv6";
            
            try
            {
                // Формируем DNS запрос (RFC 1035 / RFC 8484)
                byte[] dnsQuery = BuildDnsPacket(domain, type);
                string base64Query = Convert.ToBase64String(dnsQuery).Replace('+', '-').Replace('/', '_').TrimEnd('=');

                // Очистка адреса сервера (убираем https:// и слэши)
                string host = ((string)s.server).Replace("https://", "").TrimEnd('/');
                string url = $"https://{host}:{s.port}/dns-query?dns={base64Query}";

                using (var req = new HttpRequestMessage(HttpMethod.Get, url))
                {
                    req.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/dns-message"));
                    req.Headers.UserAgent.ParseAdd("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0");

                    using (var cts = CancellationTokenSource.CreateLinkedTokenSource(t))
                    {
                        cts.CancelAfter(TimeSpan.FromSeconds(10));
                        var resp = await _httpClient.SendAsync(req, cts.Token);
                        
                        if (resp.IsSuccessStatusCode) 
                        {
                            byte[] data = await resp.Content.ReadAsByteArrayAsync();
                            res.AddRange(ParseDnsResponse(data, type));
                        }
                        else errorList.Add($"{typeLabel}: HTTP {(int)resp.StatusCode}");
                    }
                }
            }
            catch (TaskCanceledException) { errorList.Add($"{typeLabel}: Timeout"); }
            catch (Exception ex) { errorList.Add($"{typeLabel}: {ex.Message}"); }
            
            return res;
        }

        private byte[] BuildDnsPacket(string domain, ushort type)
        {
            using (var ms = new MemoryStream())
            {
                // ID = 0000 (важно для DoH GET), Flags = 0100 (Query), QDCOUNT = 0001
                ms.Write(new byte[] { 0, 0, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0 }, 0, 12);
                foreach (var part in domain.Split('.'))
                {
                    byte[] b = Encoding.ASCII.GetBytes(part);
                    ms.WriteByte((byte)b.Length);
                    ms.Write(b, 0, b.Length);
                }
                // Terminator (0), QTYPE (type), QCLASS (1 = IN)
                ms.Write(new byte[] { 0, (byte)(type >> 8), (byte)(type & 0xff), 0, 1 }, 0, 5);
                return ms.ToArray();
            }
        }

        private List<string> ParseDnsResponse(byte[] r, ushort targetType)
        {
            var ips = new List<string>();
            try
            {
                int pos = 12; // Пропуск заголовка
                int qdc = (r[4] << 8) | r[5];
                int anc = (r[6] << 8) | r[7];

                for (int i = 0; i < qdc; i++) { SkipName(r, ref pos); pos += 4; } // Пропуск вопросов

                for (int i = 0; i < anc; i++)
                {
                    SkipName(r, ref pos);
                    if (pos + 10 > r.Length) break;
                    ushort type = (ushort)((r[pos] << 8) | r[pos + 1]);
                    pos += 8;
                    ushort dataLen = (ushort)((r[pos] << 8) | r[pos + 1]);
                    pos += 2;

                    if (type == targetType && pos + dataLen <= r.Length)
                    {
                        if (type == 1 && dataLen == 4) 
                            ips.Add($"{r[pos]}.{r[pos+1]}.{r[pos+2]}.{r[pos+3]}");
                        else if (type == 28 && dataLen == 16)
                        {
                            byte[] ipBuf = new byte[16];
                            Array.Copy(r, pos, ipBuf, 0, 16);
                            ips.Add(new IPAddress(ipBuf).ToString());
                        }
                    }
                    pos += dataLen;
                }
            } catch { }
            return ips;
        }

        private void SkipName(byte[] r, ref int p)
        {
            while (p < r.Length)
            {
                byte len = r[p];
                if (len == 0) { p++; break; }
                if ((len & 0xc0) == 0xc0) { p += 2; break; }
                p += len + 1;
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
