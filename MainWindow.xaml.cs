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
            if (!File.Exists(InputFile))
            {
                File.WriteAllText(InputFile, "# Google\r\ngoogle.com\r\n");
            }

            if (!File.Exists(SettingsFile))
            {
                File.WriteAllText(SettingsFile, "server=dns.google\r\nport=443\r\nipv4=true\r\nipv6=false\r\n");
            }
        }

        private void Log(string message)
        {
            Dispatcher.Invoke(() =>
            {
                string timestamp = DateTime.Now.ToString("HH:mm:ss");
                LogTextBox.AppendText($"[{timestamp}] {message}\r\n");
                LogTextBox.ScrollToEnd();
            });
        }

        private void BtnTheme_Click(object sender, RoutedEventArgs e)
        {
            _isDarkTheme = !_isDarkTheme;
            if (_isDarkTheme)
            {
                Resources["WindowBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#1E1E1E"));
                Resources["LogBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#252526"));
                Resources["TextColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#D4D4D4"));
                Resources["ButtonBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#333333"));
                Resources["ButtonHoverColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#3E3E42"));
            }
            else
            {
                Resources["WindowBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#F3F3F3"));
                Resources["LogBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#FFFFFF"));
                Resources["TextColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#000000"));
                Resources["ButtonBgColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#E1E1E1"));
                Resources["ButtonHoverColor"] = new SolidColorBrush((Color)ColorConverter.ConvertFromString("#D0D0D0"));
            }
        }

        private void BtnFile_Click(object sender, RoutedEventArgs e)
        {
            if (sender is System.Windows.Controls.Button btn && btn.Tag is string fileName)
            {
                if (File.Exists(fileName))
                {
                    Process.Start(new ProcessStartInfo(fileName) { UseShellExecute = true });
                }
            }
        }

        private void BtnClear_Click(object sender, RoutedEventArgs e)
        {
            LogTextBox.Clear();
            SetProgressState(0, 100, "ProgressGray");
        }

        private void BtnStop_Click(object sender, RoutedEventArgs e)
        {
            if (_cts != null && !_cts.IsCancellationRequested)
            {
                Log("Stop requested, waiting for current operation to complete...");
                _cts.Cancel();
            }
        }

        private void SetProgressState(int value, int maximum, string colorResource)
        {
            Dispatcher.Invoke(() =>
            {
                MainProgress.Maximum = maximum;
                MainProgress.Value = value;
                MainProgress.Foreground = (SolidColorBrush)FindResource(colorResource);
            });
        }

        private async void BtnStart_Click(object sender, RoutedEventArgs e)
        {
            if (_cts != null && !_cts.IsCancellationRequested) return;

            _cts = new CancellationTokenSource();
            SetProgressState(0, 100, "ProgressBlue");

            try
            {
                await Task.Run(() => ProcessDomains(_cts.Token), _cts.Token);
                SetProgressState(100, 100, "ProgressGreen");
            }
            catch (OperationCanceledException)
            {
                Log("Operation cancelled by user");
                SetProgressState(0, 100, "ProgressGray");
            }
            catch (Exception ex)
            {
                Log($"Error: {ex.Message}");
                SetProgressState(0, 100, "ProgressGray");
            }
            finally
            {
                _cts.Dispose();
                _cts = null;
            }
        }

        private async Task ProcessDomains(CancellationToken token)
        {
            Log("Starting to resolve domains...");
            Log($"Reading {SettingsFile}...");

            var settings = ReadSettings();
            Log($"DNS Server: {settings.Server}");
            Log($"IPv4: {settings.Ipv4}, IPv6: {settings.Ipv6}");

            Log($"Reading {InputFile}...");
            if (!File.Exists(InputFile)) return;

            var lines = File.ReadAllLines(InputFile);
            var outputLines = new List<string>();

            int domainsCount = lines.Count(l => !l.TrimStart().StartsWith("#") && !string.IsNullOrWhiteSpace(l));
            Log($"Found {domainsCount} domains to resolve");
            Log("----------------------------------------");

            SetProgressState(0, domainsCount == 0 ? 1 : domainsCount, "ProgressBlue");
            int processed = 0;

            foreach (var line in lines)
            {
                token.ThrowIfCancellationRequested();

                string trimmed = line.Trim();
                if (string.IsNullOrWhiteSpace(trimmed)) continue;

                if (trimmed.StartsWith("#"))
                {
                    Log(trimmed);
                    outputLines.Add(trimmed);
                    continue;
                }

                string domain = trimmed;
                Log($"Resolving: {domain}");

                var ips = new List<string>();

                if (settings.Ipv4)
                    ips.AddRange(await ResolveBinaryDoH(domain, 1, settings, token));
                
                if (settings.Ipv6)
                    ips.AddRange(await ResolveBinaryDoH(domain, 28, settings, token));

                if (ips.Count == 0)
                {
                    Log($"   No records found for {domain}");
                    outputLines.Add($"No records found: {domain}");
                }
                else
                {
                    foreach (var ip in ips.Distinct())
                    {
                        Log($"   {ip} {domain}");
                        outputLines.Add($"{ip} {domain}");
                    }
                }

                processed++;
                SetProgressState(processed, domainsCount, "ProgressBlue");
            }

            Log("----------------------------------------");
            Log($"Writing {OutputFile}...");
            File.WriteAllLines(OutputFile, outputLines);
            Log($"Successfully wrote {outputLines.Count} lines to {OutputFile}");
        }

        private (string Server, int Port, bool Ipv4, bool Ipv6) ReadSettings()
        {
            string server = "dns.google";
            int port = 443;
            bool ipv4 = true;
            bool ipv6 = false;

            if (File.Exists(SettingsFile))
            {
                foreach (var line in File.ReadAllLines(SettingsFile))
                {
                    var parts = line.Split('=');
                    if (parts.Length != 2) continue;
                    string key = parts[0].Trim().ToLower();
                    string value = parts[1].Trim().ToLower();
                    if (key == "server") server = value;
                    else if (key == "port" && int.TryParse(value, out int p)) port = p;
                    else if (key == "ipv4") ipv4 = (value == "true");
                    else if (key == "ipv6") ipv6 = (value == "true");
                }
            }
            return (server, port, ipv4, ipv6);
        }

        private async Task<List<string>> ResolveBinaryDoH(string domain, ushort qtype, (string Server, int Port, bool Ipv4, bool Ipv6) settings, CancellationToken token)
        {
            var results = new List<string>();
            try
            {
                string url = $"https://{settings.Server}:{settings.Port}/dns-query";
                byte[] queryData = BuildDnsQuery(domain, qtype);

                using (var request = new HttpRequestMessage(HttpMethod.Post, url))
                {
                    request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/dns-message"));
                    request.Content = new ByteArrayContent(queryData);
                    request.Content.Headers.ContentType = new MediaTypeHeaderValue("application/dns-message");

                    using (var response = await _httpClient.SendAsync(request, token))
                    {
                        if (response.IsSuccessStatusCode)
                        {
                            byte[] responseData = await response.Content.ReadAsByteArrayAsync();
                            results.AddRange(ParseDnsResponse(responseData, qtype));
                        }
                    }
                }
            }
            catch { }
            return results;
        }

        private byte[] BuildDnsQuery(string domain, ushort qtype)
        {
            using (var ms = new MemoryStream())
            {
                ms.WriteByte((byte)_rnd.Next(256));
                ms.WriteByte((byte)_rnd.Next(256));
                ms.WriteByte(0x01); ms.WriteByte(0x00); // Flags
                ms.WriteByte(0x00); ms.WriteByte(0x01); // QDCOUNT
                ms.Write(new byte[6], 0, 6);
                foreach (string part in domain.Split('.'))
                {
                    ms.WriteByte((byte)part.Length);
                    byte[] chars = Encoding.ASCII.GetBytes(part);
                    ms.Write(chars, 0, chars.Length);
                }
                ms.WriteByte(0);
                ms.WriteByte((byte)(qtype >> 8)); ms.WriteByte((byte)(qtype & 0xFF));
                ms.WriteByte(0x00); ms.WriteByte(0x01); // QCLASS IN
                return ms.ToArray();
            }
        }

        private List<string> ParseDnsResponse(byte[] response, ushort requestedQtype)
        {
            var ips = new List<string>();
            try
            {
                int offset = 12;
                int qdCount = (response[4] << 8) | response[5];
                int anCount = (response[6] << 8) | response[7];
                for (int i = 0; i < qdCount; i++) { SkipDnsName(response, ref offset); offset += 4; }
                for (int i = 0; i < anCount; i++)
                {
                    SkipDnsName(response, ref offset);
                    ushort type = (ushort)((response[offset] << 8) | response[offset + 1]);
                    offset += 8; // Type, Class, TTL
                    ushort dataLen = (ushort)((response[offset] << 8) | response[offset + 1]);
                    offset += 2;
                    if (type == 1 && requestedQtype == 1 && dataLen == 4)
                        ips.Add($"{response[offset]}.{response[offset + 1]}.{response[offset + 2]}.{response[offset + 3]}");
                    else if (type == 28 && requestedQtype == 28 && dataLen == 16)
                    {
                        byte[] b = new byte[16]; Array.Copy(response, offset, b, 0, 16);
                        ips.Add(new System.Net.IPAddress(b).ToString());
                    }
                    offset += dataLen;
                }
            }
            catch { }
            return ips;
        }

        private void SkipDnsName(byte[] buffer, ref int offset)
        {
            while (offset < buffer.Length)
            {
                byte len = buffer[offset];
                if (len == 0) { offset++; break; }
                if ((len & 0xC0) == 0xC0) { offset += 2; break; }
                offset += len + 1;
            }
        }
    }
}
