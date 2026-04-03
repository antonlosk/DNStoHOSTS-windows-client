using System;
using System.Windows;

namespace DNStoHOSTS
{
    public partial class App : Application
    {
        protected override void OnStartup(StartupEventArgs e)
        {
            // Перехват всех необработанных ошибок
            AppDomain.CurrentDomain.UnhandledException += (s, ex) => 
                MessageBox.Show(ex.ExceptionObject.ToString(), "Критическая ошибка");

            DispatcherUnhandledException += (s, ex) => 
            {
                MessageBox.Show(ex.Exception.ToString(), "Ошибка интерфейса");
                ex.Handled = true;
            };

            base.OnStartup(e);
        }
    }
}
